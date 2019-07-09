package schema

import (
	"reflect"
	"testing"

	"github.com/raksh93/storagetapper/db"
	"github.com/raksh93/storagetapper/log"
	"github.com/raksh93/storagetapper/test"
	"github.com/raksh93/storagetapper/types"
)

func TestAlterAvro(t *testing.T) {
	test.SkipIfNoMySQLAvailable(t)

	createTestSchemaTable(t)

	loc := &db.Loc{Service: TestSvc, Name: TestDb}

	avroSchema, err := GetAvroSchemaFromAlterTable(loc, TestTbl, "avro", `ALTER TABLE `+loc.Name+`.`+TestTbl+` ADD f111  BIGINT`)
	test.CheckFail(err, t)

	test.ExecSQL(conn, t, `ALTER TABLE `+types.MyDbName+`.`+TestTbl+` ADD f111  BIGINT`)

	avroSchemaRef, err := ConvertToAvro(loc, TestTbl, "avro")
	test.CheckFail(err, t)

	if reflect.DeepEqual(avroSchemaRef, avroSchema) {
		t.Fatalf("Schema obtained from temp table doesn't equal to the schema from direct alter")
	}

	log.Debugf("%v", avroSchemaRef)

	loc.Cluster = "please_return_nil_db_addr"
	_, err = GetAvroSchemaFromAlterTable(loc, TestTbl, "avro", `ALTER TABLE `+loc.Name+`.`+TestTbl+` ADD f111  BIGINT`)
	test.Assert(t, err != nil, "invalid DB location should fail")

	_, err = ConvertToAvro(loc, TestTbl, "avro")
	test.Assert(t, err != nil, "invalid DB location should fail")

	dropTestSchemaTable(t)
}

func TestMutateTable(t *testing.T) {
	test.SkipIfNoMySQLAvailable(t)

	createTestSchemaTable(t)

	var tblSchema types.TableSchema

	rawSchema, err := GetRaw(&db.Loc{Service: TestSvc, Name: TestDb}, TestTbl)
	test.CheckFail(err, t)

	if !MutateTable(conn, TestSvc, TestDb, TestTbl, ` ADD f111  BIGINT`, &tblSchema, &rawSchema) {
		t.Fatalf("MutateTable failed")
	}

	test.ExecSQL(conn, t, `ALTER TABLE `+types.MyDbName+`.`+TestTbl+` ADD f111  BIGINT`)

	tblSchemaRef, err := Get(&db.Loc{Service: TestSvc, Name: TestDb}, TestTbl)
	test.CheckFail(err, t)

	log.Debugf("%+v", tblSchemaRef)
	log.Debugf("%+v", tblSchema)
	if !reflect.DeepEqual(tblSchemaRef, &tblSchema) {
		t.Fatalf("Wrong mutated schema")
	}

	dropTestSchemaTable(t)
}
