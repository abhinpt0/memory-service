package serve

import (
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
)

// newGinRouter uses the escaped path for route matching, then decodes each
// captured parameter once with path semantics. Gin's built-in unescape option
// uses query semantics and incorrectly turns literal plus signs into spaces.
// The generated OpenAPI wrappers bind Gin path parameters with
// ValueIsUnescaped=true, so they must receive decoded values.
func newGinRouter() *gin.Engine {
	router := gin.New()
	router.UseEscapedPath = true
	router.UnescapePathValues = false
	router.Use(func(c *gin.Context) {
		for i := range c.Params {
			value, err := url.PathUnescape(c.Params[i].Value)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid escaped path parameter"})
				return
			}
			c.Params[i].Value = value
		}
	})
	// Make unmatched encoded paths explicit inside Gin's middleware chain so
	// the error-envelope writer cannot commit its default 200 before Gin's
	// fallback 404 runs.
	router.NoRoute(func(c *gin.Context) {
		c.Status(http.StatusNotFound)
	})
	return router
}

// setDecodedPathParam exposes the value decoded by the generated OpenAPI
// wrapper to the existing route handlers, which read path values from Gin.
func setDecodedPathParam(c *gin.Context, key, value string) {
	for i := range c.Params {
		if c.Params[i].Key == key {
			c.Params[i].Value = value
			return
		}
	}
	c.Params = append(c.Params, gin.Param{Key: key, Value: value})
}
