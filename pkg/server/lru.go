// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import "container/list"

type lruCache[K comparable, V any] struct {
	capacity int
	entries  map[K]*list.Element
	items    *list.List
}

type lruEntry[K comparable, V any] struct {
	key   K
	value V
}

func newLRUCache[K comparable, V any](capacity int) *lruCache[K, V] {
	if capacity <= 0 {
		panic("lru cache capacity must be positive")
	}

	return &lruCache[K, V]{
		capacity: capacity,
		entries:  make(map[K]*list.Element),
		items:    list.New(),
	}
}

func (c *lruCache[K, V]) Add(key K, value V) bool {
	if item, ok := c.entries[key]; ok {
		item.Value.(*lruEntry[K, V]).value = value
		c.items.MoveToFront(item)
		return false
	}

	item := c.items.PushFront(&lruEntry[K, V]{
		key:   key,
		value: value,
	})
	c.entries[key] = item

	if c.items.Len() <= c.capacity {
		return false
	}

	c.removeOldest()
	return true
}

func (c *lruCache[K, V]) Get(key K) (V, bool) {
	item, ok := c.entries[key]
	if !ok {
		var zero V
		return zero, false
	}

	c.items.MoveToFront(item)
	return item.Value.(*lruEntry[K, V]).value, true
}

func (c *lruCache[K, V]) removeOldest() {
	item := c.items.Back()
	if item == nil {
		return
	}

	c.items.Remove(item)
	delete(c.entries, item.Value.(*lruEntry[K, V]).key)
}
