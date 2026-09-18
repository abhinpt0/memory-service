package serve

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chirino/memory-service/internal/config"
	pb "github.com/chirino/memory-service/internal/generated/pb/memory/v1"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIsStreamingRequest(t *testing.T) {
	t.Run("multipart attachment upload is streaming", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/attachments", strings.NewReader("abcdef"))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=abc123")
		require.True(t, isStreamingRequest(req))
	})

	t.Run("json attachment create is not streaming", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/attachments", strings.NewReader(`{"sourceUrl":"https://example.com/file"}`))
		req.Header.Set("Content-Type", "application/json")
		require.False(t, isStreamingRequest(req))
	})

	t.Run("non-attachment endpoint is not streaming", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/evict", strings.NewReader(`{"retentionPeriod":"P90D"}`))
		req.Header.Set("Content-Type", "application/json")
		require.False(t, isStreamingRequest(req))
	})
}

func TestMaxBodySizeMiddleware_SkipsForMultipartAttachmentUpload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(maxBodySizeMiddleware(4))
	router.POST("/v1/attachments", readBodyLengthHandler)

	req := httptest.NewRequest(http.MethodPost, "/v1/attachments", strings.NewReader("0123456789"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=abc123")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "10", rec.Body.String())
}

func TestMaxBodySizeMiddleware_EnforcesForNonStreamingEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(maxBodySizeMiddleware(4))
	router.POST("/v1/admin/evict", readBodyLengthHandler)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/evict", strings.NewReader("0123456789"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestBodyReadTimeoutMiddleware_TimesOutOrdinaryBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(bodyReadTimeoutMiddleware(10*time.Millisecond, time.Minute))
	router.POST("/v1/admin/evict", readBodyNoResponseOnErrorHandler)

	body := newBlockingReadCloser()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/evict", body)
	req.ContentLength = 1
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusRequestTimeout, rec.Code)
	require.JSONEq(t, `{"code":"request_timeout","error":"request body read timeout"}`, rec.Body.String())
}

func TestBodyReadTimeoutMiddleware_UsesAttachmentTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(bodyReadTimeoutMiddleware(time.Minute, 10*time.Millisecond))
	router.POST("/v1/attachments", readBodyNoResponseOnErrorHandler)

	body := newBlockingReadCloser()
	req := httptest.NewRequest(http.MethodPost, "/v1/attachments", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=abc123")
	req.ContentLength = 1
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusRequestTimeout, rec.Code)
	require.JSONEq(t, `{"code":"request_timeout","error":"request body read timeout"}`, rec.Body.String())
}

func TestBodyReadTimeoutMiddleware_SkipsRequestsWithoutBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(bodyReadTimeoutMiddleware(time.Nanosecond, time.Nanosecond))
	router.GET("/ready", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(securityHeadersMiddleware())
	router.GET("/ok", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.Handle(http.MethodTrace, "/ok", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ok", nil))
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "strict-origin-when-cross-origin", rec.Header().Get("Referrer-Policy"))

	trace := httptest.NewRecorder()
	router.ServeHTTP(trace, httptest.NewRequest(http.MethodTrace, "/ok", nil))
	require.Equal(t, http.StatusMethodNotAllowed, trace.Code)
}

func TestMaxPageSizeMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	cfg := config.DefaultConfig()
	cfg.MaxPageSize = 3
	router.Use(maxPageSizeMiddleware(&cfg))
	router.GET("/items", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"defaultPageSize": config.ClampPageSize(c.Request.Context(), 50)})
	})

	for _, tc := range []struct {
		query string
		want  int
	}{
		{"?limit=3", http.StatusOK},
		{"?limit=4", http.StatusBadRequest},
		{"?limit=invalid", http.StatusBadRequest},
		{"?limit=0", http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items"+tc.query, nil))
		require.Equal(t, tc.want, rec.Code, tc.query)
		if tc.query == "?limit=3" {
			require.JSONEq(t, `{"defaultPageSize":3}`, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"defaultPageSize":3}`, rec.Body.String())
}

func TestMaxPageSizeUnaryInterceptor(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MaxPageSize = 3
	interceptor := maxPageSizeUnaryInterceptor(&cfg)
	handler := func(ctx context.Context, _ any) (any, error) {
		return config.ClampPageSize(ctx, 20), nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/memory.v1.EntriesService/ListEntries"}

	_, err := interceptor(context.Background(), &pb.ListEntriesRequest{
		Page: &pb.PageRequest{PageSize: 4},
	}, info, handler)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	result, err := interceptor(context.Background(), &pb.ListEntriesRequest{
		Page: &pb.PageRequest{PageSize: 3},
	}, info, handler)
	require.NoError(t, err)
	require.Equal(t, 3, result)

	_, err = interceptor(context.Background(), &pb.SearchEntriesRequest{Limit: 4}, info, handler)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func readBodyLengthHandler(c *gin.Context) {
	n, err := io.Copy(io.Discard, c.Request.Body)
	if err != nil {
		c.Status(http.StatusRequestEntityTooLarge)
		return
	}
	c.String(http.StatusOK, "%d", n)
}

func readBodyNoResponseOnErrorHandler(c *gin.Context) {
	_, err := io.Copy(io.Discard, c.Request.Body)
	if err != nil {
		return
	}
	c.Status(http.StatusNoContent)
}

type blockingReadCloser struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closed: make(chan struct{})}
}

func (b *blockingReadCloser) Read(_ []byte) (int, error) {
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingReadCloser) Close() error {
	b.once.Do(func() {
		close(b.closed)
	})
	return nil
}

func TestLoadServerCertificate_RequiresExplicitFiles(t *testing.T) {
	_, err := loadServerCertificate("", "", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "automatic self-signed certificates are disabled")

	_, err = loadServerCertificate("cert.pem", "", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires both certificate and key files")
}

func TestLoadServerCertificate_SelfSigned(t *testing.T) {
	cert, err := loadServerCertificate("", "", true)
	require.NoError(t, err)
	require.NotEmpty(t, cert.Certificate)
}

func TestFlagsIncludeOIDCTLSSkipVerify(t *testing.T) {
	cfg := config.DefaultConfig()
	flags := Flags(&cfg, NewFlagState(&cfg))

	flagNames := make(map[string]bool, len(flags))
	for _, flag := range flags {
		for _, name := range flag.Names() {
			flagNames[name] = true
		}
	}

	require.True(t, flagNames["oidc-tls-insecure-skip-verify"])
	require.True(t, flagNames["max-page-size"])
	require.True(t, flagNames["body-read-timeout"])
	require.True(t, flagNames["attachment-body-read-timeout"])
	require.True(t, flagNames["allow-non-loopback-plaintext"])
}

func TestMaxPageSizeFlagAndEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  string
		want int
	}{
		{name: "flag", args: []string{"test", "--max-page-size", "37"}, want: 37},
		{name: "environment", args: []string{"test"}, env: "41", want: 41},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			var maxPageSizeFlag cli.Flag
			for _, flag := range serverFlags(&cfg, NewFlagState(&cfg)) {
				if slices.Contains(flag.Names(), "max-page-size") {
					maxPageSizeFlag = flag
					break
				}
			}
			require.NotNil(t, maxPageSizeFlag)
			if tc.env != "" {
				t.Setenv("MEMORY_SERVICE_MAX_PAGE_SIZE", tc.env)
			}
			cmd := &cli.Command{
				Name:  "test",
				Flags: []cli.Flag{maxPageSizeFlag},
				Action: func(context.Context, *cli.Command) error {
					require.Equal(t, tc.want, cfg.MaxPageSize)
					return nil
				},
			}
			require.NoError(t, cmd.Run(context.Background(), tc.args))
		})
	}
}

func TestPolicyImportPathFlagCompatibility(t *testing.T) {
	for _, name := range []string{"MEMORY_SERVICE_POLICY_IMPORT_PATH", "MEMORY_SERVICE_POLICY_IMPORT_DIR"} {
		value, wasSet := os.LookupEnv(name)
		require.NoError(t, os.Unsetenv(name))
		t.Cleanup(func() {
			if wasSet {
				require.NoError(t, os.Setenv(name, value))
			} else {
				require.NoError(t, os.Unsetenv(name))
			}
		})
	}

	cases := []struct {
		name   string
		args   []string
		newEnv string
		oldEnv string
		want   string
	}{
		{name: "new flag", args: []string{"test", "--policy-import-path", "new-a,new-b"}, want: "new-a,new-b"},
		{name: "repeated new flag", args: []string{"test", "--policy-import-path", "new-a", "--policy-import-path", "new-b"}, want: "new-a,new-b"},
		{name: "new environment", args: []string{"test"}, newEnv: "new-env", want: "new-env"},
		{name: "old hidden flag", args: []string{"test", "--policy-import-dir", "old-flag"}, want: "old-flag"},
		{name: "old environment", args: []string{"test"}, oldEnv: "old-env", want: "old-env"},
		{name: "new flag wins", args: []string{"test", "--policy-import-path", "new", "--policy-import-dir", "old"}, want: "new"},
		{name: "new flag wins over old environment", args: []string{"test", "--policy-import-path", "new-flag"}, oldEnv: "old-env", want: "new-flag"},
		{name: "old flag wins over new environment", args: []string{"test", "--policy-import-dir", "old-flag"}, newEnv: "new-env", want: "old-flag"},
		{name: "old environment wins over new environment", args: []string{"test"}, newEnv: "new-env", oldEnv: "old-env", want: "old-env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.newEnv != "" {
				t.Setenv("MEMORY_SERVICE_POLICY_IMPORT_PATH", tc.newEnv)
			}
			if tc.oldEnv != "" {
				t.Setenv("MEMORY_SERVICE_POLICY_IMPORT_DIR", tc.oldEnv)
			}
			cfg := config.DefaultConfig()
			state := NewFlagState(&cfg)
			flags := episodicFlags(&cfg, state)
			cmd := &cli.Command{
				Name:  "test",
				Flags: flags,
				Action: func(_ context.Context, cmd *cli.Command) error {
					applyPolicyImportFlags(&cfg, cmd, state)
					require.Equal(t, tc.want, cfg.PolicyImportPath)
					return nil
				},
			}
			require.NoError(t, cmd.Run(context.Background(), tc.args))
		})
	}

	cfg := config.DefaultConfig()
	for _, flag := range episodicFlags(&cfg, NewFlagState(&cfg)) {
		if slices.Contains(flag.Names(), "policy-import-dir") {
			legacy, ok := flag.(*cli.StringFlag)
			require.True(t, ok)
			require.True(t, legacy.Hidden)
			return
		}
	}
	t.Fatal("policy-import-dir compatibility flag not found")
}

func TestPolicyImportContainerDefaultCompatibility(t *testing.T) {
	data, err := os.ReadFile("../../../Dockerfile")
	require.NoError(t, err)

	var imageDefaultName, imageDefaultValue string
	for line := range strings.Lines(string(data)) {
		name, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if found && strings.HasPrefix(name, "ENV MEMORY_SERVICE_POLICY_IMPORT_") {
			imageDefaultName = strings.TrimPrefix(name, "ENV ")
			imageDefaultValue = value
			break
		}
	}
	require.NotEmpty(t, imageDefaultName, "Dockerfile must configure the container policy import default")
	require.Equal(t, "/etc/memory-service/policies/", imageDefaultValue)

	for _, name := range []string{"MEMORY_SERVICE_POLICY_IMPORT_PATH", "MEMORY_SERVICE_POLICY_IMPORT_DIR"} {
		value, wasSet := os.LookupEnv(name)
		require.NoError(t, os.Unsetenv(name))
		t.Cleanup(func() {
			if wasSet {
				require.NoError(t, os.Setenv(name, value))
			} else {
				require.NoError(t, os.Unsetenv(name))
			}
		})
	}

	cases := []struct {
		name     string
		args     []string
		override map[string]string
		want     string
	}{
		{
			name:     "legacy environment overrides image default",
			override: map[string]string{"MEMORY_SERVICE_POLICY_IMPORT_DIR": "/custom/legacy-env"},
			want:     "/custom/legacy-env",
		},
		{
			name: "legacy flag overrides image default",
			args: []string{"test", "--policy-import-dir", "/custom/legacy-flag"},
			want: "/custom/legacy-flag",
		},
		{
			name:     "new environment overrides image default",
			override: map[string]string{"MEMORY_SERVICE_POLICY_IMPORT_PATH": "/custom/new-env"},
			want:     "/custom/new-env",
		},
		{
			name: "new flag overrides image default",
			args: []string{"test", "--policy-import-path", "/custom/new-flag"},
			want: "/custom/new-flag",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(imageDefaultName, imageDefaultValue)
			for name, value := range tc.override {
				t.Setenv(name, value)
			}

			cfg := config.DefaultConfig()
			state := NewFlagState(&cfg)
			cmd := &cli.Command{
				Name:  "test",
				Flags: episodicFlags(&cfg, state),
				Action: func(_ context.Context, cmd *cli.Command) error {
					applyPolicyImportFlags(&cfg, cmd, state)
					require.Equal(t, tc.want, cfg.PolicyImportPath)
					return nil
				},
			}
			args := tc.args
			if args == nil {
				args = []string{"test"}
			}
			require.NoError(t, cmd.Run(context.Background(), args))
		})
	}
}

func TestOIDCRoleClaimFlagLeavesUnsetValueEmpty(t *testing.T) {
	cfg := config.DefaultConfig()
	var roleClaimFlag cli.Flag
	for _, flag := range authorizationFlags(&cfg) {
		if slices.Contains(flag.Names(), "oidc-role-claim") {
			roleClaimFlag = flag
			break
		}
	}
	require.NotNil(t, roleClaimFlag)

	cmd := &cli.Command{
		Name:  "test",
		Flags: []cli.Flag{roleClaimFlag},
		Action: func(context.Context, *cli.Command) error {
			require.Empty(t, cfg.OIDCRoleClaims)
			return nil
		},
	}
	require.NoError(t, cmd.Run(context.Background(), []string{"test"}))
}
