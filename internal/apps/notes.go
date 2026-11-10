// notes.go holds what installers print after an install (and `info` prints
// again): URLs and logins, always read live from the cluster.

package apps

import (
	"context"
	"fmt"
)

// printURL prints how to reach an exposed app: its routed URLs, or the
// port-forward to use when it is not routed.
// Lifecycle and info call it before an app's notes, so notes only add what is
// specific to the app.
func printURL(env *Env, app App, host string) {
	if app.Expose == nil {
		return
	}
	if host != "" {
		fmt.Fprintf(env.Out, "   URL:      %s  ·  %s\n", env.URL("http", host), env.URL("https", host))
		return
	}
	local := app.Expose.LocalPort
	if local == 0 {
		local = app.Expose.Port
	}
	fmt.Fprintf(env.Out, "   URL:      http://localhost:%d  (after: kubectl --context %s port-forward -n %s svc/%s %d:%d)\n",
		local, env.KubeContext, app.Namespace, app.Expose.Service, local, app.Expose.Port)
}

// printCredentials prints a username/password pair read from a Secret.
func printCredentials(ctx context.Context, env *Env, ns, secret, userKey, passKey string) {
	user := env.secretValue(ctx, ns, secret, userKey)
	pass := env.secretValue(ctx, ns, secret, passKey)
	if user == "" && pass == "" {
		fmt.Fprintf(env.Out, "   Login:    (secret %s/%s not found)\n", ns, secret)
		return
	}
	fmt.Fprintf(env.Out, "   Username: %s\n   Password: %s\n", user, pass)
}

// printAccess prints everything about reaching an installed app: its URL,
// then the app's own notes (logins, CLI hints).
func printAccess(ctx context.Context, env *Env, app App, host string) {
	printURL(env, app, host)
	if app.notes != nil {
		app.notes(ctx, env, host)
	}
}
