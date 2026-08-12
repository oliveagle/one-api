package router

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/logger"
)

// SetAgentInstallRouter registers the public static route serving
// config.AgentInstallDir at /agent-install/* when the directory exists.
//
// Behaviour:
//   - If AgentInstallDir is empty (env disabled), skip registration entirely.
//     (Install commands that hit /agent-install/* then hit the SPA / 404
//     fallback like any other unknown path -- which is the same behaviour
//     as not having the feature at all.)
//   - If AgentInstallDir is set but the directory does not exist or is not
//     readable, log a warning and skip. We never panic at startup.
//   - If the directory exists, mount it. Files inside are served verbatim,
//     no auth, no rate limiting (the operator controls the contents).
//
// We register BEFORE SetWebRouter in SetRouter so the static handler wins over
// the SPA fallback for files that exist on disk. The /agent-install path
// does NOT overlap with /api, /v1 or the frontend mount, so existing routes
// are unaffected.
func SetAgentInstallRouter(router *gin.Engine) error {
	dir := strings.TrimSpace(config.AgentInstallDir)
	if dir == "" {
		logger.SysLog("agent install serving disabled: AGENT_INSTALL_DIR is empty")
		return nil
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		logger.SysLogf("agent install serving skipped: cannot resolve %q: %v", dir, err)
		return nil
	}

	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			logger.SysLogf("agent install serving skipped: directory %q does not exist (set AGENT_INSTALL_DIR or create the directory)", abs)
		} else {
			logger.SysLogf("agent install serving skipped: cannot stat %q: %v", abs, err)
		}
		return nil
	}
	if !info.IsDir() {
		logger.SysLogf("agent install serving skipped: %q is not a directory", abs)
		return nil
	}

	prefix := config.AgentInstallURLPrefix
	logger.SysLogf("agent install serving enabled: %s/* -> %s", prefix, abs)
	// Use a hand-rolled handler rather than static.Serve so we return a
	// proper 404 for missing files instead of letting gin's empty 200 fall
	// through. static.LocalFile uses gin.Dir underneath which already
	// blocks path-traversal escapes (gin.Dir resolves ".." against root).
	fs := static.LocalFile(abs, false)
	handler := func(c *gin.Context) {
		// fs.Exists verifies that a file exists under the mount (it also
		// checks index.html for directories when indexes is false).
		if !fs.Exists(prefix, c.Request.URL.Path) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		http.StripPrefix(prefix, http.FileServer(fs)).ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
	router.GET(prefix+"/*filepath", handler)
	router.GET(prefix, handler)
	return nil
}
