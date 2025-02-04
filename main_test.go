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
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shopify/sarama"

	"github.com/raksh93/storagetapper/config"
	"github.com/raksh93/storagetapper/db"
	"github.com/raksh93/storagetapper/encoder"
	"github.com/raksh93/storagetapper/log"
	"github.com/raksh93/storagetapper/pipe"
	"github.com/raksh93/storagetapper/schema"
	"github.com/raksh93/storagetapper/shutdown"
	"github.com/raksh93/storagetapper/state"
	"github.com/raksh93/storagetapper/test"
	"github.com/raksh93/storagetapper/types"
	"github.com/raksh93/storagetapper/util"
)

/*SQL command and expected test result, both for Avro and JSON formats*/
var resfmt = [][]string{
	{ //Insert some data before start the service, it'll be snapshotted
		"INSERT INTO e2e_test_table1(f1) VALUES (?)",
		`{"Type":"insert","Key":[%d],"SeqNo":%d,"Timestamp":0,"Fields":[{"Name":"f1","Value":%d},{"Name":"f3","Value":0},{"Name":"f4","Value":null}]}`,
		`{"Name":"hp.e2e_test_db1-e2e_test_table1","Fields":[{"Name":"hp.f1","Datum":%d},{"Name":"hp.f3","Datum":0},{"Name":"hp.f4","Datum":null},{"Name":"hp.ref_key","Datum":%d},{"Name":"hp.row_key","Datum":"%s"},{"Name":"hp.is_deleted","Datum":false}]}`,
	},
	{ //Some basic inserts to be processed by binlog
		"INSERT INTO e2e_test_table1(f1) VALUES (?)",
		`{"Type":"insert","Key":[%d],"SeqNo":%d,"Timestamp":0,"Fields":[{"Name":"f1","Value":%d},{"Name":"f3","Value":0},{"Name":"f4","Value":null}]}`,
		`{"Name":"hp.e2e_test_db1-e2e_test_table1","Fields":[{"Name":"hp.f1","Datum":%d},{"Name":"hp.f3","Datum":0},{"Name":"hp.f4","Datum":null},{"Name":"hp.ref_key","Datum":%d},{"Name":"hp.row_key","Datum":"%s"},{"Name":"hp.is_deleted","Datum":false}]}`,
	},
	{ //Test schema change by adding f2 field
		"INSERT INTO e2e_test_table1(f1,f2) VALUES (?, CONCAT('bbb', ?))",
		`{"Type":"insert","Key":[%d],"SeqNo":%d,"Timestamp":0,"Fields":[{"Name":"f1","Value":%d},{"Name":"f3","Value":0},{"Name":"f4","Value":null},{"Name":"f2","Value":"bbb%d"}]}`,
		/*FIXME: There is no way to notify streamer to update schema currently, so
		* testing only case which fits initial schema*/
		`{"Name":"hp.e2e_test_db1-e2e_test_table1","Fields":[{"Name":"hp.f1","Datum":%d},{"Name":"hp.f3","Datum":0},{"Name":"hp.f4","Datum":null},{"Name":"hp.ref_key","Datum":%d},{"Name":"hp.row_key","Datum":"%s"},{"Name":"hp.is_deleted","Datum":false}]}`,
	},

	{ //Test updates, which generated records pair: delete/insert
		"UPDATE e2e_test_table1 SET f3=f3+20 WHERE f1>? AND f1<?",
		`{"Type":"delete","Key":[%d],"SeqNo":%d,"Timestamp":0}`,
		`{"Type":"insert","Key":[%d],"SeqNo":%d,"Timestamp":0,"Fields":[{"Name":"f1","Value":%d},{"Name":"f3","Value":20},{"Name":"f4","Value":null}]}`,
	},

	{ //This is continuation for the above update
		"",
		`{"Name":"hp.e2e_test_db1-e2e_test_table1","Fields":[{"Name":"hp.f1","Datum":null},{"Name":"hp.f3","Datum":null},{"Name":"hp.f4","Datum":null},{"Name":"hp.ref_key","Datum":%d},{"Name":"hp.row_key","Datum":"%s"},{"Name":"hp.is_deleted","Datum":true}]}`,
		`{"Name":"hp.e2e_test_db1-e2e_test_table1","Fields":[{"Name":"hp.f1","Datum":%d},{"Name":"hp.f3","Datum":20},{"Name":"hp.f4","Datum":null},{"Name":"hp.ref_key","Datum":%d},{"Name":"hp.row_key","Datum":"%s"},{"Name":"hp.is_deleted","Datum":false}]}`,
	},
	{ //Insert some data before start the service, it'll be snapshotted
		"INSERT INTO e2e_test_table1(f1) VALUES (?)",
		`{"Type":"insert","Key":[%d],"SeqNo":%d,"Timestamp":0,"Fields":[{"Name":"f1","Value":%d},{"Name":"f3","Value":0},{"Name":"f4","Value":null}]}`,
		`{"Name":"hp.e2e_test_db1-e2e_test_table1","Fields":[{"Name":"hp.f1","Datum":%d},{"Name":"hp.f3","Datum":0},{"Name":"hp.f4","Datum":null},{"Name":"hp.ref_key","Datum":%d},{"Name":"hp.row_key","Datum":"%s"},{"Name":"hp.is_deleted","Datum":false}]}`,
	},
}

var resfmt2 = [][]string{
	{ //Insert some data before start the service, it'll be snapshotted
		"INSERT INTO e2e_test_table2(f1) VALUES (?)",
		`{"Type":"insert","Key":[%d],"SeqNo":%d,"Timestamp":0,"Fields":[{"Name":"f1","Value":%d},{"Name":"f3","Value":0},{"Name":"f4","Value":null}]}`,
		`{"Name":"hp.e2e_test_db1-e2e_test_table2","Fields":[{"Name":"hp.f1","Datum":%d},{"Name":"hp.f3","Datum":0},{"Name":"hp.f4","Datum":null},{"Name":"hp.ref_key","Datum":%d},{"Name":"hp.row_key","Datum":"%s"},{"Name":"hp.is_deleted","Datum":false}]}`,
	},
	{ //Some basic inserts to be processed by binlog
		"INSERT INTO e2e_test_table2(f1) VALUES (?)",
		`{"Type":"insert","Key":[%d],"SeqNo":%d,"Timestamp":0,"Fields":[{"Name":"f1","Value":%d},{"Name":"f3","Value":0},{"Name":"f4","Value":null}]}`,
		`{"Name":"hp.e2e_test_db1-e2e_test_table2","Fields":[{"Name":"hp.f1","Datum":%d},{"Name":"hp.f3","Datum":0},{"Name":"hp.f4","Datum":null},{"Name":"hp.ref_key","Datum":%d},{"Name":"hp.row_key","Datum":"%s"},{"Name":"hp.is_deleted","Datum":false}]}`,
	},
}

func waitSnapshotToFinish(init bool, table string, t *testing.T) {
	if !init {
		return
	}
	for !shutdown.Initiated() {
		n, err := state.GetTableNewFlag("e2e_test_svc1", "e2e_test_cluster1", "e2e_test_db1", table, "mysql", "kafka", 0)
		test.CheckFail(err, t)
		if !n {
			break
		}
		time.Sleep(time.Millisecond * 1000)
	}
	time.Sleep(time.Millisecond * 500)

	log.Debugf("Detected snapshot finished for %v", table)
}

func consumeEvents(c pipe.Consumer, format string, avroResult []string, jsonResult []string, outEncoder encoder.Encoder, t *testing.T) {
	result := jsonResult

	for _, v := range result {
		//We can't notify "avro" encoder about schema changed by "ALTER"
		//so remove new "f2" field "avro" encoded doesn't know of
		if format == "avro" {
			var r types.CommonFormatEvent
			err := json.Unmarshal([]byte(v), &r)
			test.CheckFail(err, t)

			if r.Fields != nil {
				k := 0
				for k < len(*r.Fields) && (*r.Fields)[k].Name != "f2" {
					k++
				}
				if k < len(*r.Fields) {
					//"f2" field, if present, should be last field in the fields array
					*r.Fields = (*r.Fields)[:k]
				}
			}

			if format == "avro" && r.Type == "delete" {
				key := encoder.GetCommonFormatKey(&r)
				r.Key = make([]interface{}, 0)
				r.Key = append(r.Key, key)
			}

			b, err := json.Marshal(r)
			v = string(b)
			test.CheckFail(err, t)
		}
		if !c.FetchNext() {
			return
		}
		msg, err := c.Pop()
		test.CheckFail(err, t)

		var b []byte
		if format == "msgpack" || format == "avro" {
			r, err := outEncoder.DecodeEvent(msg.([]byte))
			test.CheckFail(err, t)
			b, err = json.Marshal(r)
			test.CheckFail(err, t)
		} else {
			b = msg.([]byte)
		}

		//		log.Errorf("Received : %+v %v", string(b), len(b))
		//		log.Errorf("Reference: %+v %v", v, len(v))
		conv := string(b)
		if v != conv {
			log.Errorf("Received : %+v %v", conv, len(b))
			log.Errorf("Reference: %+v %v", v, len(v))
			t.FailNow()
		}
	}
}

var seqno int

func waitMainInitialized() {
	/*Wait while it initializes */
	for shutdown.NumProcs() < 2 {
		time.Sleep(time.Millisecond * 500)
	}
}

func waitAllEventsStreamed(format string, c pipe.Consumer, sseqno int, seqno int) int {
	//Only json and msgpack push schema to stream, skip schema events for others
	if format != "json" && format != "msgpack" {
		sseqno++
	}

	for ; sseqno < seqno; sseqno++ {
		_ = c.FetchNext()
	}

	return sseqno
}

func initTestDB(init bool, t *testing.T) {
	if init {
		conn, err := db.Open(&db.Addr{Host: "localhost", Port: 3306, User: types.TestMySQLUser, Pwd: types.TestMySQLPassword, Db: ""})
		test.CheckFail(err, t)

		test.ExecSQL(conn, t, "DROP DATABASE IF EXISTS "+types.MyDbName)
		test.ExecSQL(conn, t, "DROP DATABASE IF EXISTS e2e_test_db1")
		test.ExecSQL(conn, t, "RESET MASTER")

		test.ExecSQL(conn, t, "CREATE DATABASE e2e_test_db1")
		test.ExecSQL(conn, t, "CREATE TABLE e2e_test_db1.e2e_test_table1(f1 int not null primary key, f3 int not null default 0, f4 int)")

		err = conn.Close()
		test.CheckFail(err, t)
	}
}

func addTable(init bool, format string, tableNum string, output string, t *testing.T) {
	table := "e2e_test_table" + tableNum

	log.Debugf("aaaaa %v %v", output, format)
	if init {
		/*Insert some test cluster connection info */
		if tableNum == "1" {
			err := util.HTTPPostJSON("http://localhost:7836/cluster", `{"cmd" : "add", "name" : "e2e_test_cluster1", "host" : "localhost", "port" : 3306, "user" : "`+types.TestMySQLUser+`", "pw" : "`+types.TestMySQLPassword+`"}`)
			test.CheckFail(err, t)
		}
		/*Register test table for ingestion */
		err := util.HTTPPostJSON("http://localhost:7836/table", `{"cmd" : "add", "cluster" : "e2e_test_cluster1", "service" : "e2e_test_svc1", "db":"e2e_test_db1", "table":"`+table+`","output":"`+output+`","OutputFormat":"`+format+`"}`)
		test.CheckFail(err, t)

		/*Insert output Avro schema */
		dst := `, "dst" : "local"`
		err = util.HTTPPostJSON("http://localhost:7836/schema", `{"cmd" : "register", "service" : "e2e_test_svc1", "db" : "e2e_test_db1", "type" : "`+format+`", "table" : "`+table+`"`+dst+` }`)
		test.CheckFail(err, t)

		if tableNum == "1" {
			conn, err := db.Open(&db.Addr{Host: "localhost", Port: 3306, User: types.TestMySQLUser, Pwd: types.TestMySQLPassword, Db: ""})
			test.CheckFail(err, t)
			test.ExecSQL(conn, t, "UPDATE "+types.MyDbName+".state SET seqno=? WHERE tableName=?", seqno-1000000, table)
			err = conn.Close()
			test.CheckFail(err, t)
		}
	}
}

func prepareReferenceArray2(conn *sql.DB, keyShift int, resfmt []string, jsonResult2 *[]string, avroResult2 *[]string, t *testing.T) {
	for i := 1 + keyShift; i < 10+keyShift; i++ {
		test.ExecSQL(conn, t, resfmt[0], i)
		seqno++
		s := fmt.Sprintf(resfmt[1], i, seqno, i)
		*jsonResult2 = append(*jsonResult2, s)
		//Number before strconv is number of digits in i
		keyLen := strconv.Itoa(len(strconv.Itoa(i)))
		s = fmt.Sprintf(resfmt[2], i, seqno, base64.StdEncoding.EncodeToString([]byte(keyLen+strconv.Itoa(i))))
		*avroResult2 = append(*avroResult2, s)
	}
}

func prepareReferenceArray(conn *sql.DB, init bool, keyShift int, resfmt []string, jsonResult *[]string, avroResult *[]string, t *testing.T) {
	for i := 101 + keyShift; i < 110+keyShift; i++ {
		test.ExecSQL(conn, t, resfmt[0], i)
		sq := 0
		if !init {
			seqno++
			sq = seqno
		}
		s := fmt.Sprintf(resfmt[1], i, sq, i)
		*jsonResult = append(*jsonResult, s)
		keyLen := strconv.Itoa(len(strconv.Itoa(i)))
		s = fmt.Sprintf(resfmt[2], i, sq, base64.StdEncoding.EncodeToString([]byte(keyLen+strconv.Itoa(i))))
		*avroResult = append(*avroResult, s)
	}
}

func createSecondTable(init bool, conn *sql.DB, t *testing.T) {
	if init {
		test.ExecSQL(conn, t, "CREATE TABLE e2e_test_db1.e2e_test_table2(f1 int not null primary key, f3 int not null default 0, f4 int)")
	}
}

func testStep(inPipeType string, bufferFormat string, outPipeType string, outPipeFormat string, init bool, keyShift int, t *testing.T) {
	cfg := config.Get()

	cfg.DataDir = "/tmp/storagetapper/main_test"
	cfg.MaxFileSize = 1 // file per message
	cfg.ChangelogPipeType = inPipeType
	cfg.InternalEncoding = bufferFormat
	//json and msgpack pushes schema not wrapped into transport format, so
	//streamer won't be able to decode it unless outPipeFormat =
	//InternalEncoding = bufferFormat
	if outPipeFormat == "json" || outPipeFormat == "msgpack" {
		cfg.InternalEncoding = outPipeFormat
		var err error
		encoder.Internal, err = encoder.InitEncoder(cfg.InternalEncoding, "", "", "")
		test.CheckFail(err, t)
	}
	cfg.StateUpdateTimeout = 1
	cfg.MaxNumProcs = 3
	//	cfg.PipeBatchSize = 1
	schema.HeatpipeNamespace = "hp"
	encoder.GenTime = func() int64 { return 0 }

	log.Configure(cfg.LogType, cfg.LogLevel, false)

	log.Debugf("STARTED STEP: %v, %v, %v, %v, %v, %v, seqno=%v", inPipeType, bufferFormat, outPipeType, outPipeFormat, init, keyShift, seqno)

	var jsonResult, avroResult []string = make([]string, 0), make([]string, 0)
	var jsonResult2, avroResult2 []string = make([]string, 0), make([]string, 0)

	trackingPipe, err := pipe.Create(shutdown.Context, outPipeType, cfg.PipeBatchSize, cfg, nil)
	test.CheckFail(err, t)
	trackingConsumer, err := trackingPipe.NewConsumer("hp-tap-e2e_test_svc1-e2e_test_db1-e2e_test_table1")
	test.CheckFail(err, t)

	initTestDB(init, t)

	seqno += 1000000
	sseqno := seqno

	if init && outPipeFormat != "avro" {
		jsonResult = append(jsonResult, fmt.Sprintf(`{"Type":"schema","Key":["f1"],"SeqNo":%d,"Timestamp":0,"Fields":[{"Name":"f1","Value":"int(11)"},{"Name":"f3","Value":"int(11)"},{"Name":"f4","Value":"int(11)"}]}`, 0))
	}

	conn, err := db.Open(&db.Addr{Host: "localhost", Port: 3306, User: types.TestMySQLUser, Pwd: types.TestMySQLPassword, Db: "e2e_test_db1"})
	test.CheckFail(err, t)

	prepareReferenceArray(conn, init, keyShift, resfmt[0], &jsonResult, &avroResult, t)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		mainLow(cfg)
		wg.Done()
	}()

	waitMainInitialized()

	defer func() {
		shutdown.Initiate()
		shutdown.Wait() //There is no guarantee that this wait return after main's Wait that's why below wait for local wg
		wg.Wait()
		log.Debugf("FINISHED STEP: %v, %v, %v, %v", inPipeType, bufferFormat, outPipeType, outPipeFormat)
	}()

	/*New consumer sees only new events, so register it before any events
	* produced */
	p, err := pipe.Create(shutdown.Context, outPipeType, cfg.PipeBatchSize, cfg, nil)
	test.CheckFail(err, t)
	c, err := p.NewConsumer("hp-tap-e2e_test_svc1-e2e_test_db1-e2e_test_table1")
	test.CheckFail(err, t)
	c2, err := p.NewConsumer("hp-tap-e2e_test_svc1-e2e_test_db1-e2e_test_table2")
	test.CheckFail(err, t)

	addTable(init, outPipeFormat, "1", outPipeType, t)

	outEncoder, err := encoder.Create(outPipeFormat, "e2e_test_svc1", "e2e_test_db1", "e2e_test_table1")
	test.CheckFail(err, t)

	/*Wait snapshot to finish before sending more data otherwise everything
	* even following events will be read from snapshot and we want them to be
	* read from binlog */
	waitSnapshotToFinish(init, "e2e_test_table1", t)

	for i := 1 + keyShift; i < 10+keyShift; i++ {
		test.ExecSQL(conn, t, resfmt[1][0], i)
		seqno++
		s := fmt.Sprintf(resfmt[1][1], i, seqno, i)
		jsonResult = append(jsonResult, s)
		//Number before strconv is number of digits in i
		keyLen := strconv.Itoa(len(strconv.Itoa(i)))
		s = fmt.Sprintf(resfmt[1][2], i, seqno, base64.StdEncoding.EncodeToString([]byte(keyLen+strconv.Itoa(i))))
		avroResult = append(avroResult, s)
	}

	//This makes sure that both changelog reader and streamer has read old
	//schema
	sseqno = waitAllEventsStreamed(outPipeFormat, trackingConsumer, sseqno, seqno)

	test.ExecSQL(conn, t, "ALTER TABLE e2e_test_table1 ADD f2 varchar(32)")
	seqno++
	if outPipeFormat != "avro" {
		jsonResult = append(jsonResult, fmt.Sprintf(`{"Type":"schema","Key":["f1"],"SeqNo":%d,"Timestamp":0,"Fields":[{"Name":"f1","Value":"int(11)"},{"Name":"f3","Value":"int(11)"},{"Name":"f4","Value":"int(11)"},{"Name":"f2","Value":"varchar(32)"}]}`, seqno))
	}

	for i := 11 + keyShift; i < 30+keyShift; i++ {
		test.ExecSQL(conn, t, resfmt[2][0], i, i)
		seqno++
		s := fmt.Sprintf(resfmt[2][1], i, seqno, i, i)
		jsonResult = append(jsonResult, s)
		keyLen := strconv.Itoa(len(strconv.Itoa(i)))
		s = fmt.Sprintf(resfmt[2][2], i, seqno, base64.StdEncoding.EncodeToString([]byte(keyLen+strconv.Itoa(i))))
		avroResult = append(avroResult, s)
	}

	waitAllEventsStreamed(outPipeFormat, trackingConsumer, sseqno, seqno)

	test.ExecSQL(conn, t, "ALTER TABLE e2e_test_table1 DROP f2")
	seqno++
	if outPipeFormat != "avro" {
		jsonResult = append(jsonResult, fmt.Sprintf(`{"Type":"schema","Key":["f1"],"SeqNo":%d,"Timestamp":0,"Fields":[{"Name":"f1","Value":"int(11)"},{"Name":"f3","Value":"int(11)"},{"Name":"f4","Value":"int(11)"}]}`, seqno))
	}

	test.ExecSQL(conn, t, resfmt[3][0], 100+keyShift, 111+keyShift)
	for i := 101 + keyShift; i < 110+keyShift; i++ {
		seqno++
		s := fmt.Sprintf(resfmt[3][1], i, seqno)
		jsonResult = append(jsonResult, s)
		keyLen := strconv.Itoa(len(strconv.Itoa(i)))
		s = fmt.Sprintf(resfmt[4][1], seqno, base64.StdEncoding.EncodeToString([]byte(keyLen+strconv.Itoa(i))))
		avroResult = append(avroResult, s)

		seqno++
		s = fmt.Sprintf(resfmt[3][2], i, seqno, i)
		jsonResult = append(jsonResult, s)
		keyLen = strconv.Itoa(len(strconv.Itoa(i)))
		s = fmt.Sprintf(resfmt[4][2], i, seqno, base64.StdEncoding.EncodeToString([]byte(keyLen+strconv.Itoa(i))))
		avroResult = append(avroResult, s)
	}

	createSecondTable(init, conn, t)

	if init && outPipeFormat != "avro" {
		jsonResult2 = append(jsonResult2, fmt.Sprintf(`{"Type":"schema","Key":["f1"],"SeqNo":%d,"Timestamp":0,"Fields":[{"Name":"f1","Value":"int(11)"},{"Name":"f3","Value":"int(11)"},{"Name":"f4","Value":"int(11)"}]}`, 0))
	}

	prepareReferenceArray(conn, init, keyShift, resfmt2[0], &jsonResult2, &avroResult2, t)

	/*Wait while binlog skips above inserts before registering table*/
	for {
		gtid, err1 := state.GetGTID(&db.Loc{Cluster: "e2e_test_cluster1", Service: "e2e_test_svc1", Name: "e2e_test_db1"})
		test.CheckFail(err1, t)
		log.Debugf("Binlog reader at %v, waiting after :1-75", gtid)
		i := strings.LastIndex(gtid, "-")
		if i == -1 {
			t.FailNow()
		}
		j, err2 := strconv.ParseInt(gtid[i+1:], 10, 32)
		test.CheckFail(err2, t)
		if j > 75 {
			break
		}
		time.Sleep(time.Millisecond * 50)
	}

	addTable(init, outPipeFormat, "2", outPipeType, t)

	outEncoder2, err := encoder.Create(outPipeFormat, "e2e_test_svc1", "e2e_test_db1", "e2e_test_table2")
	test.CheckFail(err, t)

	log.Debugf("Inserted second table")

	st, err := state.Get()
	test.CheckFail(err, t)
	log.Debugf("%v", st)

	/*Wait snapshot to finish before sending more data otherwise everything
	* even following events will be read from snapshot and we want them to be
	* read from binlog */
	waitSnapshotToFinish(init, "e2e_test_table2", t)

	prepareReferenceArray2(conn, keyShift, resfmt2[1], &jsonResult2, &avroResult2, t)
	prepareReferenceArray2(conn, keyShift+1000, resfmt[5], &jsonResult, &avroResult, t)
	/*
		for i := 1 + keyShift; i < 10+keyShift; i++ {
			test.ExecSQL(conn, t, resfmt2[1][0], i)
			seqno++
			s := fmt.Sprintf(resfmt2[1][1], i, seqno, i)
			jsonResult2 = append(jsonResult2, s)
			//Number before strconv is number of digits in i
			keyLen := strconv.Itoa(len(strconv.Itoa(i)))
			s = fmt.Sprintf(resfmt2[1][2], i, seqno, base64.StdEncoding.EncodeToString([]byte(keyLen+strconv.Itoa(i))))
			avroResult2 = append(avroResult2, s)
		}

		for i := 1 + keyShift; i < 10+keyShift; i++ {
			test.ExecSQL(conn, t, resfmt[5][0], i+1000)
			seqno++
			s := fmt.Sprintf(resfmt[5][1], i+1000, seqno, i+1000)
			jsonResult = append(jsonResult, s)
			//Number before strconv is number of digits in i
			keyLen := strconv.Itoa(len(strconv.Itoa(i + 1000)))
			s = fmt.Sprintf(resfmt[5][2], i+1000, seqno, base64.StdEncoding.EncodeToString([]byte(keyLen+strconv.Itoa(i+1000))))
			avroResult = append(avroResult, s)
		}
	*/

	time.Sleep(time.Millisecond * 1500) //Let binlog to see table deletion

	log.Debugf("Start consuming events from %v", "hp-tap-e2e_test_svc1-e2e_test_db1-e2e_test_table1")
	consumeEvents(c, outPipeFormat, avroResult, jsonResult, outEncoder, t)
	log.Debugf("Start consuming events from %v", "hp-tap-e2e_test_svc1-e2e_test_db1-e2e_test_table2")
	consumeEvents(c2, outPipeFormat, avroResult2, jsonResult2, outEncoder2, t)

	test.CheckFail(c.Close(), t)
	test.CheckFail(c2.Close(), t)

	log.Debugf("FINISHING STEP: %v, %v, %v, %v", inPipeType, bufferFormat, outPipeType, outPipeFormat)
}

func TestBasic(t *testing.T) {
	_ = test.LoadConfig()

	test.SkipIfNoMySQLAvailable(t)
	test.SkipIfNoKafkaAvailable(t)

	//FIXME: Rewrite test so it doesn't require events to come out inorder
	//Configure producer so as everything will go to one partition
	pipe.InitialOffset = pipe.OffsetNewest
	pipe.KafkaConfig = sarama.NewConfig()
	pipe.KafkaConfig.Producer.Partitioner = sarama.NewManualPartitioner
	pipe.KafkaConfig.Producer.Return.Successes = true
	pipe.KafkaConfig.Consumer.MaxWaitTime = 10 * time.Millisecond
	pipe.Delimited = true

	for _, o := range []string{"kafka"} {
		//for o := range pipe.Pipes { //FIXME: file and hdfs fail some time
		if o == "local" {
			continue
		}
		for _, b := range []string{"local", "kafka"} {
			for _, i := range []string{"json", "msgpack"} { //possible buffer formats
				for _, enc := range encoder.Encoders() {
					testStep(b, i, o, enc, true, 0, t)
					testStep(b, i, o, enc, false, 100000, t)
				}
			}
		}
	}
}
