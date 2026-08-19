package testmongo

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/mongodb"
)

// StartMongo starts a disposable MongoDB container and returns its connection URI.
func StartMongo(tb testing.TB) string {
	tb.Helper()
	return startMongo(tb, true)
}

// StartStandaloneMongo starts a MongoDB topology without transaction support.
func StartStandaloneMongo(tb testing.TB) string {
	tb.Helper()
	return startMongo(tb, false)
}

func startMongo(tb testing.TB, replicaSet bool) string {
	tb.Helper()

	ctx := context.Background()
	var container *mongodb.MongoDBContainer
	var err error
	if replicaSet {
		container, err = mongodb.Run(ctx, "mongo:7", mongodb.WithReplicaSet("rs0"))
	} else {
		container, err = mongodb.Run(ctx, "mongo:7")
	}
	if err != nil {
		tb.Fatalf("start mongodb container: %v", err)
	}

	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := container.Terminate(ctx); err != nil {
			tb.Errorf("terminate mongodb container: %v", err)
		}
	})

	uri, err := container.ConnectionString(ctx)
	if err != nil {
		tb.Fatalf("build mongodb connection string: %v", err)
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		tb.Fatalf("parse mongodb connection string: %v", err)
	}
	if replicaSet {
		query := parsed.Query()
		// On Docker Desktop the single-node replica set advertises its container IP,
		// which is not routable from the host. Direct connection keeps discovery on
		// the mapped host port while retaining replica-set transaction support.
		query.Set("directConnection", "true")
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}
