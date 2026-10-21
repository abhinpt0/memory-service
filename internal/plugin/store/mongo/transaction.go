//go:build !nomongo

package mongo

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

const mongoTransactionTimeout = 120 * time.Second

// runMongoTransactionOnce runs fn exactly once. Commit can be retried when the
// server's commit result is unknown, but the application callback is never
// replayed. Use this for callbacks that can call external systems.
func runMongoTransactionOnce(ctx context.Context, client *mongo.Client, fn func(context.Context) error) error {
	session, err := client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)

	if err := session.StartTransaction(); err != nil {
		return err
	}
	err = mongo.WithSession(ctx, session, fn)
	if err != nil {
		_ = session.AbortTransaction(context.WithoutCancel(ctx))
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = session.AbortTransaction(context.WithoutCancel(ctx))
		return err
	}

	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mongoTransactionTimeout)
	defer cancel()
	for {
		err = session.CommitTransaction(commitCtx)
		if err == nil {
			return nil
		}
		var commandErr mongo.CommandError
		if !errors.As(err, &commandErr) ||
			!commandErr.HasErrorLabel("UnknownTransactionCommitResult") ||
			commandErr.IsMaxTimeMSExpiredError() || commitCtx.Err() != nil {
			return err
		}
	}
}
