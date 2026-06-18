package dbconn

import (
	"context"
	"fmt"
	"time"
)

// ConnectOpts tunes database dial behaviour for Windows deployments.
type ConnectOpts struct {
	TimeoutSec           int
	MssqlEncrypt         bool
	MssqlTrustServerCert bool
}

var defaultConnectOpts = ConnectOpts{
	TimeoutSec:           30,
	MssqlEncrypt:         true,
	MssqlTrustServerCert: true,
}

func SetDefaultConnectOpts(o ConnectOpts) {
	defaultConnectOpts = o.normalized()
}

func DefaultConnectOpts() ConnectOpts {
	return defaultConnectOpts.normalized()
}

func ConnectOptsFromSettings(timeoutSec int, mssqlEncrypt, mssqlTrustServerCert bool) ConnectOpts {
	return ConnectOpts{
		TimeoutSec:           timeoutSec,
		MssqlEncrypt:         mssqlEncrypt,
		MssqlTrustServerCert: mssqlTrustServerCert,
	}.normalized()
}

func (o ConnectOpts) normalized() ConnectOpts {
	if o.TimeoutSec <= 0 {
		o.TimeoutSec = 30
	}
	return o
}

func withConnectTimeout(ctx context.Context, opts ConnectOpts) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	d := time.Duration(opts.TimeoutSec) * time.Second
	return context.WithTimeout(ctx, d)
}

func mssqlEncryptQuery(opts ConnectOpts) string {
	if opts.MssqlEncrypt {
		return "true"
	}
	return "false"
}

func mssqlTrustCertQuery(opts ConnectOpts) string {
	if opts.MssqlTrustServerCert {
		return "true"
	}
	return "false"
}

func oracleTimeoutOptions(opts ConnectOpts) map[string]string {
	return map[string]string{
		"CONNECTION TIMEOUT": fmt.Sprintf("%d", opts.TimeoutSec),
	}
}
