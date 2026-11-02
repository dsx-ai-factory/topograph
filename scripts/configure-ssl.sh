#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2024 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -e

SSL_DIR="${SSL_DIR:-/etc/topograph/ssl}"

CA_KEY="${SSL_DIR}/ca-key.pem"
CA_CERT="${SSL_DIR}/ca-cert.pem"

SRV_KEY="${SSL_DIR}/server-key.pem"
SRV_CSR="${SSL_DIR}/server.csr"
SRV_CERT="${SSL_DIR}/server-cert.pem"

mkdir -p "${SSL_DIR}"
# Generate encrypted CA private key
openssl genrsa -out ${CA_KEY} 4096
# Create self-signed root certificate
openssl req -x509 -new -nodes -key ${CA_KEY} -sha512 -out ${CA_CERT} -subj "/CN=*" -days 365
# Create server private key
openssl genrsa -out ${SRV_KEY} 4096
# Create Certificate Signing Request
openssl req -new -key ${SRV_KEY} -out ${SRV_CSR} -subj "/O=nvidia/CN=localhost"
# Sign CSR with CA
openssl x509 -req -in ${SRV_CSR} -CA ${CA_CERT} -CAkey ${CA_KEY} -CAcreateserial -out ${SRV_CERT} -sha512 -days 365
# Verify certificates
openssl verify -CAfile ${CA_CERT} ${SRV_CERT}
# Print certificate expirations
echo
echo "Validity of ${CA_CERT} :"
openssl x509 -in ${CA_CERT} -noout -dates
echo
echo "Validity of ${SRV_CERT} :"
openssl x509 -in ${SRV_CERT} -noout -dates
