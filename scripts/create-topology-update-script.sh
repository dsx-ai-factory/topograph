#!/bin/bash

#
# This script creates the topology update script and registers it with 'strigger'
#

set -eo pipefail

if [ "$UID" -ne 0 ]; then
    echo "Must run as root."
    exit 1
fi

SCRIPT_PATH="update-topology-config.sh"
PROVIDER=""
TOPOLOGY_CONFIG_PATH=""
SUPPORTED_PROVIDERS=(aws gcp oci nebius netq nscale lambdai firebird infiniband-bm)
if [[ ! -z "$SLURM_CONF" ]]; then 
  TOPOLOGY_CONFIG_PATH="$(dirname $SLURM_CONF)/topology.conf"
fi

#
# print script description and usage
#
function usage() {
  cat << EOF

Usage: $(basename $0) [options]
Options:
  -s <path to the generated topology update script>
    Default: $SCRIPT_PATH
  -c <path to the topology config>
    Default: $TOPOLOGY_CONFIG_PATH
  -p <provider>
    Options: ${SUPPORTED_PROVIDERS[*]}
  -h
    Print this message
EOF
}

### MAIN

while getopts 's:c:p:h' opt; do
  case "$opt" in
    s)
      SCRIPT_PATH=$OPTARG
      ;;
    c)
      TOPOLOGY_CONFIG_PATH=$OPTARG
      ;;
    p)
      PROVIDER=$OPTARG
      ;;
    h)
      usage
      exit 0
      ;;
    ?)
      usage
      exit 1
      ;;
  esac
done

if [[ -z "$TOPOLOGY_CONFIG_PATH" ]]; then
  echo "topology config file is not set"
  usage
  exit 1
fi

if [[ -z "$PROVIDER" ]]; then
  echo "provider is not set"
  usage
  exit 1
fi

provider_supported=false
for supported_provider in "${SUPPORTED_PROVIDERS[@]}"; do
  if [[ "$PROVIDER" == "$supported_provider" ]]; then
    provider_supported=true
    break
  fi
done

if [[ "$provider_supported" != true ]]; then
  echo "unsupported provider $PROVIDER"
  usage
  exit 1
fi

echo "Creating $SCRIPT_PATH to update topology config $TOPOLOGY_CONFIG_PATH"
echo ""

cat <<EOF > $SCRIPT_PATH
#!/bin/bash

curl -X POST -H "Content-Type: application/json" -d '{"provider":{"name":"$PROVIDER"},"engine":{"name":"slurm","params":{"topologyConfigPath":"$TOPOLOGY_CONFIG_PATH","reconfigure":true}}}' http://localhost:49021/v1/generate
EOF

chmod 755 $SCRIPT_PATH

echo "Registering $SCRIPT_PATH with stigger"
echo ""

su - slurm -c "strigger --set --node --down --up --flags=perm --program=$(realpath $SCRIPT_PATH)"

echo "Done"
