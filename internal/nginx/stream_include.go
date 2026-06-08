package nginx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// nginx's `conf.d/*.conf` include lives inside `http{}`, so a stream{}
// block can't be loaded from there — it has to live at main scope in
// `/etc/nginx/nginx.conf` itself. ensureStreamInclude injects (or
// removes) a marker-delimited stream{} block that includes
// `/etc/nginx/conf.d/apigw-stream.conf`. Idempotent.
//
// Pattern mirrors internal/tuning's marker block — same `.apigw-prev`
// backup convention so operators can diff or roll back.

const (
	streamMarkerStart = "# >>> apigw stream >>> DO NOT EDIT — managed by apigw"
	streamMarkerEnd   = "# <<< apigw stream <<<"
)

var streamBlockRE = regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(streamMarkerStart) + `\n.*?^` + regexp.QuoteMeta(streamMarkerEnd) + `\n?`)

// streamConfPathFunc lets tests override the target. Default returns
// "/etc/nginx/nginx.conf" (or APIGW_NGINX_CONF when set, matching the
// tuning package).
var streamNginxConfPath = func() string {
	if v := os.Getenv("APIGW_NGINX_CONF"); v != "" {
		return v
	}
	return "/etc/nginx/nginx.conf"
}

// ensureStreamInclude makes the live `nginx.conf` either contain or
// lack the apigw-stream{} include block. `want=true` adds it (idempotent),
// `want=false` removes it (idempotent). On first add, snapshots
// nginx.conf to <path>.apigw-prev.
func ensureStreamInclude(want bool) error {
	path := streamNginxConfPath()
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No nginx.conf to update — caller (likely a unit test or
			// custom layout) handles main-scope includes itself.
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	has := streamBlockRE.Match(body)
	if want == has {
		return nil
	}
	var out []byte
	if want {
		block := []byte(streamMarkerStart + "\n" +
			"stream {\n" +
			"    include " + StreamConfPath + ";\n" +
			"}\n" +
			streamMarkerEnd + "\n")
		// Insert AFTER the http{} block — nginx requires stream{} at main
		// scope. We place it at the end of the file rather than parsing
		// http{} so we don't accidentally land inside another block.
		if !bytes.HasSuffix(body, []byte("\n")) {
			out = append(body, '\n')
		} else {
			out = append([]byte{}, body...)
		}
		out = append(out, '\n')
		out = append(out, block...)
	} else {
		out = streamBlockRE.ReplaceAll(body, nil)
	}
	// Snapshot once before the first edit.
	bak := path + BackupExtension
	if _, statErr := os.Stat(bak); os.IsNotExist(statErr) {
		if werr := os.WriteFile(bak, body, 0o644); werr != nil {
			return fmt.Errorf("write backup %s: %w", bak, werr)
		}
	}
	return streamWriteAtomic(path, out)
}

func streamWriteAtomic(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		_ = os.Remove(name)
	}()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
