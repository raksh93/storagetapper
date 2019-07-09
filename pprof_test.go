// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package main

import (
	"sync"
	"testing"
	"time"

	"github.com/raksh93/storagetapper/db"
	"github.com/raksh93/storagetapper/shutdown"
	"github.com/raksh93/storagetapper/test"
	"github.com/raksh93/storagetapper/types"
	"github.com/raksh93/storagetapper/util"

	_ "net/http/pprof"
)

func TestPprofBasic(t *testing.T) {
	cfg := test.LoadConfig()

	test.SkipIfNoMySQLAvailable(t)

	conn, err := db.Open(&db.Addr{Host: "localhost", Port: 3306, User: types.TestMySQLUser, Pwd: types.TestMySQLPassword})
	test.CheckFail(err, t)

	test.ExecSQL(conn, t, "DROP DATABASE IF EXISTS "+types.MyDbName)
	test.ExecSQL(conn, t, "DROP DATABASE IF EXISTS e2e_test_db1")
	test.ExecSQL(conn, t, "RESET MASTER")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		mainLow(cfg)
		wg.Done()
	}()

	/*Wait while it initializes */
	for shutdown.NumProcs() <= 1 {
		time.Sleep(time.Millisecond * 500)
	}

	_, err = util.HTTPGet("http://localhost:7836/debug/pprof/trace?seconds=1")
	test.CheckFail(err, t)
	_, err = util.HTTPGet("http://localhost:7836/debug/pprof/profile?seconds=1")
	test.CheckFail(err, t)
	_, err = util.HTTPGet("http://localhost:7836/debug/pprof/heap")
	test.CheckFail(err, t)

	shutdown.Initiate()
	shutdown.Wait()
	wg.Wait()
}
