package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	osexec "os/exec"
	"runtime"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/apiclient"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// browserLogin logs in through the browser (see internal/core/cli_login.go):
// it starts a login on server, shows the link and the code to type there,
// opens the browser when it can and waits for a user to approve. The
// result carries the new token, its scope and who approved it.
func (g *globals) browserLogin(ctx context.Context, server string, open, platform bool) (apitypes.CLILoginWait, error) {
	api, err := apiclient.New(server, "")
	if err != nil {
		return apitypes.CLILoginWait{}, err
	}
	host, _ := os.Hostname()
	var start apitypes.CLILoginStart
	req := apitypes.CLILoginStartRequest{ClientName: "dock CLI", Hostname: host, OS: runtime.GOOS + "/" + runtime.GOARCH, Platform: platform}
	if err := api.JSON(ctx, http.MethodPost, apiPrefix+"/auth/cli", req, &start); err != nil {
		return apitypes.CLILoginWait{}, err
	}
	page, err := url.Parse(start.VerificationURL)
	if err != nil || (page.Scheme != "http" && page.Scheme != "https") {
		return apitypes.CLILoginWait{}, &output.Error{Exit: output.ExitIncompatible, Message: fmt.Sprintf("the server sent an invalid login link %q", start.VerificationURL)}
	}

	// The link and code are what to act on, so they show even with --json
	// (on stderr; stdout stays one JSON value).
	_, _ = fmt.Fprintf(g.io.Err, "Open %s and enter the code %s\n\n", page, start.UserCode)
	if open && canOpenBrowser() && openBrowser(page.String()) == nil {
		g.io.Progressf("Opened your browser")
	}
	g.io.Progressf("Waiting for approval until %s (Ctrl-C to cancel)", start.ExpiresAt.Local().Format("15:04"))

	for {
		var res apitypes.CLILoginWait
		err := api.JSON(ctx, http.MethodPost, apiPrefix+"/auth/cli/wait", apitypes.CLILoginWaitRequest{DeviceCode: start.DeviceCode}, &res)
		switch {
		case err == nil && res.Status == "approved":
			return res, nil
		case err == nil: // still pending: the server held the request ~30s
			continue
		case errors.Is(err, context.Canceled):
			return res, &output.Error{Exit: output.ExitError, Message: "login cancelled"}
		}
		e, ok := errors.AsType[*output.Error](err)
		switch {
		case ok && e.Code == string(apitypes.CodeDenied):
			return res, &output.Error{Exit: output.ExitAuth, Code: e.Code, Message: "the login was denied in the browser", Hint: "Run `dock login` again if that was a mistake.", Err: err}
		case ok && e.Code == string(apitypes.CodeExpired):
			return res, &output.Error{Exit: output.ExitAuth, Code: e.Code, Message: "the login link expired before it was approved", Hint: "Run `dock login` again; links last 10 minutes.", Err: err}
		case ok && e.Exit == output.ExitUnreachable && time.Now().Before(start.ExpiresAt):
			// The daemon restarted or a proxy cut the request: the login
			// survives both, so try again shortly.
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return res, &output.Error{Exit: output.ExitError, Message: "login cancelled"}
			}
		default:
			return res, err
		}
	}
}

// revokeOwnToken revokes token on server, for tokens `dock login` got in
// the browser. A token that no longer works counts as revoked.
func revokeOwnToken(ctx context.Context, server, token string) error {
	api, err := apiclient.New(server, token)
	if err != nil {
		return err
	}
	err = api.JSON(ctx, http.MethodDelete, apiPrefix+"/me/token", nil, nil)
	if e, ok := errors.AsType[*output.Error](err); ok && e.Exit == output.ExitAuth {
		return nil
	}
	return err
}

// canOpenBrowser reports whether a browser would open where the user
// sits: not over SSH, and on Linux and BSD only in a graphical session.
func canOpenBrowser() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// openBrowser opens link (an http or https URL) without waiting for the
// browser.
func openBrowser(link string) error {
	var cmd *osexec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = osexec.Command("open", link)
	case "windows":
		cmd = osexec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		cmd = osexec.Command("xdg-open", link)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
