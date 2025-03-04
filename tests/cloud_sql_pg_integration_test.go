//go:build integration && cloudsqlpg

//
// Copyright 2024 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tests

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/cloudsqlconn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	CLOUD_SQL_POSTGRES_SOURCE_KIND = "cloud-sql-postgres"
	CLOUD_SQL_POSTGRES_TOOL_KIND   = "postgres-sql"
	CLOUD_SQL_POSTGRES_PROJECT     = os.Getenv("CLOUD_SQL_POSTGRES_PROJECT")
	CLOUD_SQL_POSTGRES_REGION      = os.Getenv("CLOUD_SQL_POSTGRES_REGION")
	CLOUD_SQL_POSTGRES_INSTANCE    = os.Getenv("CLOUD_SQL_POSTGRES_INSTANCE")
	CLOUD_SQL_POSTGRES_DATABASE    = os.Getenv("CLOUD_SQL_POSTGRES_DATABASE")
	CLOUD_SQL_POSTGRES_USER        = os.Getenv("CLOUD_SQL_POSTGRES_USER")
	CLOUD_SQL_POSTGRES_PASS        = os.Getenv("CLOUD_SQL_POSTGRES_PASS")
)

func getCloudSQLPgVars(t *testing.T) map[string]any {
	switch "" {
	case CLOUD_SQL_POSTGRES_PROJECT:
		t.Fatal("'CLOUD_SQL_POSTGRES_PROJECT' not set")
	case CLOUD_SQL_POSTGRES_REGION:
		t.Fatal("'CLOUD_SQL_POSTGRES_REGION' not set")
	case CLOUD_SQL_POSTGRES_INSTANCE:
		t.Fatal("'CLOUD_SQL_POSTGRES_INSTANCE' not set")
	case CLOUD_SQL_POSTGRES_DATABASE:
		t.Fatal("'CLOUD_SQL_POSTGRES_DATABASE' not set")
	case CLOUD_SQL_POSTGRES_USER:
		t.Fatal("'CLOUD_SQL_POSTGRES_USER' not set")
	case CLOUD_SQL_POSTGRES_PASS:
		t.Fatal("'CLOUD_SQL_POSTGRES_PASS' not set")
	}

	return map[string]any{
		"kind":     CLOUD_SQL_POSTGRES_SOURCE_KIND,
		"project":  CLOUD_SQL_POSTGRES_PROJECT,
		"instance": CLOUD_SQL_POSTGRES_INSTANCE,
		"region":   CLOUD_SQL_POSTGRES_REGION,
		"database": CLOUD_SQL_POSTGRES_DATABASE,
		"user":     CLOUD_SQL_POSTGRES_USER,
		"password": CLOUD_SQL_POSTGRES_PASS,
	}
}

// Copied over from cloud_sql_pg.go
func initCloudSQLPgConnectionPool(project, region, instance, ip_type, user, pass, dbname string) (*pgxpool.Pool, error) {
	// Configure the driver to connect to the database
	dsn := fmt.Sprintf("user=%s password=%s dbname=%s sslmode=disable", user, pass, dbname)
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("unable to parse connection uri: %w", err)
	}

	// Create a new dialer with options
	dialOpts, err := GetCloudSQLDialOpts(ip_type)
	if err != nil {
		return nil, err
	}
	d, err := cloudsqlconn.NewDialer(context.Background(), cloudsqlconn.WithDefaultDialOptions(dialOpts...))
	if err != nil {
		return nil, fmt.Errorf("unable to parse connection uri: %w", err)
	}

	// Tell the driver to use the Cloud SQL Go Connector to create connections
	i := fmt.Sprintf("%s:%s:%s", project, region, instance)
	config.ConnConfig.DialFunc = func(ctx context.Context, _ string, instance string) (net.Conn, error) {
		return d.Dial(ctx, i)
	}

	// Interact with the driver directly as you normally would
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, err
	}
	return pool, nil
}

func TestCloudSQLPgSimpleToolEndpoints(t *testing.T) {
	sourceConfig := getCloudSQLPgVars(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var args []string

	pool, err := initCloudSQLPgConnectionPool(CLOUD_SQL_POSTGRES_PROJECT, CLOUD_SQL_POSTGRES_REGION, CLOUD_SQL_POSTGRES_INSTANCE, "public", CLOUD_SQL_POSTGRES_USER, CLOUD_SQL_POSTGRES_PASS, CLOUD_SQL_POSTGRES_DATABASE)
	if err != nil {
		t.Fatalf("unable to create Cloud SQL connection pool: %s", err)
	}

	// create table name with UUID
	tableNameParam := "param_table_" + strings.Replace(uuid.New().String(), "-", "", -1)
	tableNameAuth := "auth_table_" + strings.Replace(uuid.New().String(), "-", "", -1)

	// set up data for param tool
	create_statement1, insert_statement1, tool_statement1, params1 := GetPostgresSQLParamToolInfo(tableNameParam)
	teardownTable1 := SetupPostgresSQLTable(t, ctx, pool, create_statement1, insert_statement1, tableNameParam, params1)
	defer teardownTable1(t)

	// set up data for auth tool
	create_statement2, insert_statement2, tool_statement2, params2 := GetPostgresSQLAuthToolInfo(tableNameAuth)
	teardownTable2 := SetupPostgresSQLTable(t, ctx, pool, create_statement2, insert_statement2, tableNameAuth, params2)
	defer teardownTable2(t)

	// Write config into a file and pass it to command
	toolsFile := GetToolsConfig(sourceConfig, CLOUD_SQL_POSTGRES_TOOL_KIND, tool_statement1, tool_statement2)

	cmd, cleanup, err := StartCmd(ctx, toolsFile, args...)
	if err != nil {
		t.Fatalf("command initialization returned an error: %s", err)
	}
	defer cleanup()

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := cmd.WaitForString(waitCtx, regexp.MustCompile(`Server ready to serve`))
	if err != nil {
		t.Logf("toolbox command logs: \n%s", out)
		t.Fatalf("toolbox didn't start successfully: %s", err)
	}

	RunToolGetTest(t)

	select_1_want := "[{\"?column?\":1}]"
	RunToolInvokeTest(t, select_1_want)
}

// Test connection with different IP type
func TestCloudSQLPgIpConnection(t *testing.T) {
	sourceConfig := getCloudSQLPgVars(t)

	tcs := []struct {
		name   string
		ipType string
	}{
		{
			name:   "public ip",
			ipType: "public",
		},
		{
			name:   "private ip",
			ipType: "private",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			sourceConfig["ipType"] = tc.ipType
			RunSourceConnectionTest(t, sourceConfig, CLOUD_SQL_POSTGRES_TOOL_KIND)
		})
	}
}
