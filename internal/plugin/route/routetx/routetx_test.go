package routetx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBufferedWritePublishesOnlyCommittedAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	attempt := 0
	err := bufferedWrite(c, func(run func(context.Context) error) error {
		attempt++
		require.NoError(t, run(context.Background()))
		assert.Empty(t, recorder.Body.String())
		attempt++
		require.NoError(t, run(context.Background()))
		assert.Empty(t, recorder.Body.String())
		return nil
	}, func(context.Context) error {
		c.Header("X-Attempt", strconv.Itoa(attempt))
		c.JSON(http.StatusCreated, gin.H{"attempt": attempt})
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, recorder.Code)
	assert.Equal(t, "2", recorder.Header().Get("X-Attempt"))
	assert.JSONEq(t, `{"attempt":2}`, recorder.Body.String())
}

func TestBufferedWriteDiscardsResponseWhenCommitFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	commitErr := errors.New("commit failed")

	err := bufferedWrite(c, func(run func(context.Context) error) error {
		require.NoError(t, run(context.Background()))
		assert.Empty(t, recorder.Body.String())
		return commitErr
	}, func(context.Context) error {
		c.JSON(http.StatusCreated, gin.H{"status": "created"})
		return nil
	})

	require.ErrorIs(t, err, commitErr)
	assert.Empty(t, recorder.Body.String())
	c.JSON(http.StatusInternalServerError, gin.H{"status": "failed"})
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.JSONEq(t, `{"status":"failed"}`, recorder.Body.String())
}
