// Copyright 2014 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License. See the AUTHORS file
// for names of contributors.
//
// Author: Spencer Kimball (spencer.kimball@gmail.com)

package kv

import (
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/cockroach/proto"
	"github.com/cockroachdb/cockroach/storage"
	"github.com/cockroachdb/cockroach/storage/engine"
	"github.com/cockroachdb/cockroach/util"
	"github.com/cockroachdb/cockroach/util/log"
)

// startTestWriter creates a writer which intiates a sequence of
// transactions, each which writes up to 10 times to random keys
// with random values.
func startTestWriter(db storage.DB, i int64, pause time.Duration, wg *sync.WaitGroup,
	retries *int32, done <-chan struct{}, t *testing.T) {
	src := rand.New(rand.NewSource(i))
	for {
		select {
		case <-done:
			if wg != nil {
				wg.Done()
			}
			return
		default:
			txnOpts := &storage.TransactionOptions{
				Name: fmt.Sprintf("concurrent test %d", i),
				Retry: &util.RetryOptions{
					Backoff:    1 * time.Millisecond,
					MaxBackoff: 10 * time.Millisecond,
					Constant:   2,
				},
			}
			first := true
			err := db.RunTransaction(txnOpts, func(txn storage.DB) error {
				if !first && retries != nil {
					atomic.AddInt32(retries, int32(1))
				}
				first = false
				for j := 0; j <= int(src.Int31n(10)); j++ {
					key := []byte(util.RandString(src, 10))
					val := []byte(util.RandString(src, int(src.Int31n(1<<8))))
					putR := <-txn.Put(&proto.PutRequest{RequestHeader: proto.RequestHeader{Key: key}, Value: proto.Value{Bytes: val}})
					if putR.GoError() != nil {
						log.Infof("experienced an error in routine %d: %s", i, putR.GoError())
						return putR.GoError()
					}
				}
				return nil
			})
			if err != nil {
				t.Error(err)
			} else if pause != 0 {
				time.Sleep(pause)
			}
		}
	}
}

// TestRangeSplitsWithConcurrentTxns does 5 consecutive splits while
// 10 concurrent goroutines, each running successive transactions
// composed of a random mix of puts.
func TestRangeSplitsWithConcurrentTxns(t *testing.T) {
	db, _, _ := createTestDB(t)
	defer db.Close()

	// This channel shuts the whole apparatus down.
	done := make(chan struct{})

	// Compute the split keys.
	const splits = 5
	splitKeys := []engine.Key(nil)
	for i := 0; i < splits; i++ {
		splitKeys = append(splitKeys, engine.Key(fmt.Sprintf("%02d", i)))
	}

	// Start up the concurrent goroutines which run transactions.
	const concurrency = 10
	var retries int32
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go startTestWriter(db, int64(i), 1*time.Millisecond, &wg, &retries, done, t)
	}

	// Execute the consecutive splits.
	for _, splitKey := range splitKeys {
		time.Sleep(5 * time.Millisecond) // allow some time for transactions to make progress
		log.Infof("starting split at key %q..", splitKey)
		splitR := <-db.AdminSplit(&proto.AdminSplitRequest{RequestHeader: proto.RequestHeader{Key: splitKey}, SplitKey: splitKey})
		if splitR.GoError() != nil {
			t.Fatal(splitR.GoError())
		}
		log.Infof("split at key %q complete", splitKey)
	}

	close(done)
	wg.Wait()

	if retries != 0 {
		t.Errorf("expected no retries splitting a range with concurrent writes, "+
			"as range splits do not cause conflicts; got %d", retries)
	}
}

// TestRangeSplitsWithWritePressure sets the zone config max bytes for
// a range to 1K and writes data until there are five ranges.
func TestRangeSplitsWithWritePressure(t *testing.T) {
	db, _, _ := createTestDB(t)
	defer db.Close()
	txnOpts := &storage.TransactionOptions{
		Name: "scan meta2 records",
		Retry: &util.RetryOptions{
			Backoff:    1 * time.Millisecond,
			MaxBackoff: 10 * time.Millisecond,
			Constant:   2,
		},
	}

	// Rewrite a zone config with low max bytes.
	zoneConfig := &proto.ZoneConfig{
		ReplicaAttrs: []proto.Attributes{
			proto.Attributes{},
			proto.Attributes{},
			proto.Attributes{},
		},
		RangeMinBytes: 1 << 8,
		RangeMaxBytes: 1 << 10,
	}
	if err := storage.PutProto(db, engine.MakeKey(engine.KeyConfigZonePrefix, engine.KeyMin), zoneConfig); err != nil {
		t.Fatal(err)
	}

	// Start test writer.
	done := make(chan struct{})
	go startTestWriter(db, int64(0), 500*time.Microsecond, nil, nil, done, t)

	// Check that we split 5 times with (a very generous for slow test machines) 500ms.
	if err := util.IsTrueWithin(func() bool {
		// Scan the txn records (in a txn due to possible retries) to see number of ranges.
		var kvs []proto.KeyValue
		if err := db.RunTransaction(txnOpts, func(txn storage.DB) error {
			scanR := <-txn.Scan(&proto.ScanRequest{
				RequestHeader: proto.RequestHeader{
					Key:    engine.KeyMeta2Prefix,
					EndKey: engine.KeyMetaMax,
				},
			})
			if scanR.GoError() != nil {
				return scanR.GoError()
			}
			kvs = scanR.Rows
			return nil
		}); err != nil {
			t.Fatalf("failed to scan meta1 keys: %s", err)
		}
		return len(kvs) >= 5
	}, 500*time.Millisecond); err != nil {
		t.Errorf("failed to split 5 times: %s", err)
	}
	close(done)
}
