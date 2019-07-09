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

package test

import (
	"database/sql"
	"fmt"
	"os"
	"path"
	"runtime"
	"testing"

	"github.com/Shopify/sarama"

	"github.com/raksh93/storagetapper/config"
	"github.com/raksh93/storagetapper/db"
	"github.com/raksh93/storagetapper/log"
	"github.com/raksh93/storagetapper/metrics"
	"github.com/raksh93/storagetapper/types"
	"github.com/raksh93/storagetapper/util"
)

var cfg *config.AppConfig

//Failer introduced to allow both testing.T and testing.B in CheckFail call
type Failer interface {
	FailNow()
	Skip(...interface{})
}

// ExecSQL executes SQL and logs on error.
func ExecSQL(db *sql.DB, t Failer, query string, param ...interface{}) {
	CheckFail(util.ExecSQL(db, query, param...), t)
}

// CheckFail fails the test if error is set, logs file, line, func of the failure
// location
func CheckFail(err error, t Failer) {
	if err != nil {
		pc, file, no, _ := runtime.Caller(1)
		details := runtime.FuncForPC(pc)
		log.Fatalf("%v:%v %v: Test failed: %v", path.Base(file), no, path.Base(details.Name()), err.Error())
		t.FailNow()
	}
}

//MySQLAvailable test if local MySQL instance is running
func mysqlAvailable() bool {
	d, err := db.Open(&db.Addr{Host: "localhost", Port: 3306, User: types.TestMySQLUser, Pwd: types.TestMySQLPassword})
	if err != nil {
		return false
	}
	if err := d.Close(); err != nil {
		return false
	}
	return true
}

func kafkaAvailable() bool {
	producer, err := sarama.NewSyncProducer(cfg.KafkaAddrs, nil)
	if err != nil {
		return false
	}
	_ = producer.Close()
	return true
}

//SkipIfNoKafkaAvailable tries to connect to local Kafka and if fails, then skip
//the test
func SkipIfNoKafkaAvailable(t Failer) {
	if !kafkaAvailable() {
		t.Skip("No local Kafka detected")
	}
}

//SkipIfNoMySQLAvailable tries to connect to local MySQL and if fails, then skip
//the test
func SkipIfNoMySQLAvailable(t Failer) {
	if !mysqlAvailable() {
		t.Skip("No local MySQL detected")
	}
}

// Assert fails the test if cond is false, logs file, line, func of the failure
// location
func Assert(t *testing.T, cond bool, msg string, param ...interface{}) {
	if !cond {
		pc, file, no, _ := runtime.Caller(1)
		details := runtime.FuncForPC(pc)
		args := []interface{}{path.Base(file), no, path.Base(details.Name())}
		args = append(args, param...)
		t.Fatalf("%v:%v %v: "+msg, args...)
	}
}

// LoadConfig loads config for testing environment
func LoadConfig() *config.AppConfig {
	cfg = config.Get()
	if cfg == nil {
		fmt.Fprintf(os.Stderr, "Can't load config")
	}

	log.Configure(cfg.LogType, cfg.LogLevel, config.EnvProduction())

	err := metrics.Init()
	log.F(err)

	db.GetInfo = db.GetInfoForTest

	log.Debugf("Config: %+v", cfg)

	return cfg
}
