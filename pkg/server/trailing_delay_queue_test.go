/*
 * Copyright 2024 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package server

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
	"github.com/dsx-ai-factory/topograph/pkg/topology"
)

func TestRepeatingPayload(t *testing.T) {
	var counter int32

	processItem := func(item any) (any, *httperr.Error) {
		atomic.AddInt32(&counter, 1)
		return nil, nil
	}

	queue := NewTrailingDelayQueue(processItem, 500*time.Millisecond)

	request := &topology.Request{
		Provider: topology.Provider{Name: "test"},
		Engine: topology.Engine{
			Name:   "test",
			Params: map[string]any{"a": 1, "b": 2, "c": 3, "d": 4},
		},
	}
	for cycle := 1; cycle <= 2; cycle++ {
		for range 3 {
			_, err := queue.Submit(request)
			require.NoError(t, err)
			time.Sleep(100 * time.Millisecond)
		}

		time.Sleep(time.Second)
		val := int(atomic.LoadInt32(&counter))
		require.Equal(t, cycle, val)
	}

	queue.Shutdown()
}

func TestVaryingPayload(t *testing.T) {

	processItem := func(item any) (any, *httperr.Error) {
		return item, nil
	}

	queue := NewTrailingDelayQueue(processItem, 500*time.Millisecond)

	submissions := [3]string{}
	for i := range len(submissions) {
		request := &topology.Request{
			Provider: topology.Provider{Name: "test"},
			Engine: topology.Engine{
				Name:   "test",
				Params: map[string]any{"a": i, "b": 2, "c": 3, "d": 4},
			},
		}
		uid, err := queue.Submit(request)
		require.NoError(t, err)
		submissions[i] = uid
		time.Sleep(100 * time.Millisecond)
	}

	for i := 1; i < len(submissions); i++ {
		require.NotEqual(t, submissions[i], submissions[i-1])
	}

	time.Sleep(time.Second)
	for i := 0; i < len(submissions); i++ {
		res := queue.Get(submissions[i])
		require.NotNil(t, res)
		require.Equal(t, http.StatusOK, res.Status)
		require.Equal(t, i, res.Ret.(*topology.Request).Engine.Params["a"])
	}

	queue.Shutdown()
}

func TestLRU(t *testing.T) {
	cache := newLRUCache[int, int](3)

	_, ok := cache.Get(1)
	require.False(t, ok) // not found

	require.False(t, cache.Add(1, 1)) // w/o eviction
	require.False(t, cache.Add(2, 2)) // w/o eviction

	v, ok := cache.Get(1)
	require.True(t, ok) // found
	require.Equal(t, 1, v)

	require.False(t, cache.Add(3, 3)) // w/o eviction
	require.True(t, cache.Add(4, 4))  // with eviction of LRU "2"

	_, ok = cache.Get(2)
	require.False(t, ok) // not found

	v, ok = cache.Get(1)
	require.True(t, ok) // found
	require.Equal(t, 1, v)
}

type trailingDelayQueueTestItem struct {
	hash string
}

func (i trailingDelayQueueTestItem) Hash() (string, error) {
	return i.hash, nil
}

func TestSubmitRunningCallbackDoesNotDeleteReplacementTimer(t *testing.T) {
	const hash = "same-request"

	started := make(chan struct{})
	unblock := make(chan struct{})
	returned := make(chan struct{})
	var calls int32

	queue := NewTrailingDelayQueue(func(item any) (any, *httperr.Error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
			<-unblock
			close(returned)
		}
		return item, nil
	}, 10*time.Millisecond)
	defer queue.Shutdown()

	_, err := queue.Submit(trailingDelayQueueTestItem{hash: hash})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)

	queue.delay = time.Hour
	_, err = queue.Submit(trailingDelayQueueTestItem{hash: hash})
	require.NoError(t, err)

	queue.mutex.Lock()
	replacement := queue.timers[hash]
	queue.mutex.Unlock()
	require.NotNil(t, replacement)

	close(unblock)
	<-returned
	time.Sleep(50 * time.Millisecond)

	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	require.Same(t, replacement, queue.timers[hash])
}

func TestGetReturnsCompletionSnapshot(t *testing.T) {
	const hash = "same-request"

	started := make(chan struct{})
	unblock := make(chan struct{})

	queue := NewTrailingDelayQueue(func(item any) (any, *httperr.Error) {
		close(started)
		<-unblock
		return item, nil
	}, 10*time.Millisecond)
	defer queue.Shutdown()

	uid, err := queue.Submit(trailingDelayQueueTestItem{hash: hash})
	require.NoError(t, err)
	require.Equal(t, hash, uid)

	require.Eventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)

	pending := queue.Get(uid)
	require.Equal(t, http.StatusAccepted, pending.Status)
	require.Nil(t, pending.Ret)

	close(unblock)
	require.Eventually(t, func() bool {
		return queue.Get(uid).Status == http.StatusOK
	}, time.Second, 10*time.Millisecond)

	require.Equal(t, http.StatusAccepted, pending.Status)
	require.Nil(t, pending.Ret)
}
