package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/npmania/mvad/internal/config"
)

// resolveSplitNets builds the split set's source addresses: the raw
// prefixes plus fresh resolutions of the recorded docker and compose
// entries. Container addresses change across restarts, so entries are
// names resolved at use, never stored addresses.
func resolveSplitNets(cfg *config.Config) ([]netip.Prefix, []error) {
	nets := parseNets(cfg.SplitNets)
	var errs []error
	for _, name := range cfg.SplitDocker {
		ps, err := dockerNets(name)
		if err == nil && len(ps) == 0 {
			err = errors.New("no address; is it running?")
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("docker:%s: %w", name, err))
			continue
		}
		nets = append(nets, ps...)
	}
	for _, entry := range cfg.SplitCompose {
		project, service, _ := strings.Cut(entry, "/")
		ps, err := composeNets(project, service)
		if err != nil {
			errs = append(errs, fmt.Errorf("compose:%s: %w", entry, err))
			continue
		}
		nets = append(nets, ps...)
	}
	return nets, errs
}

func dockerNets(container string) ([]netip.Prefix, error) {
	out, err := dockerOutput("inspect", "--type", "container", "--format",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}} {{.GlobalIPv6Address}} {{end}}", container)
	if err != nil {
		return nil, err
	}
	var ps []netip.Prefix
	for _, f := range strings.Fields(string(out)) {
		a, err := netip.ParseAddr(f)
		if err != nil {
			continue
		}
		ps = append(ps, netip.PrefixFrom(a, a.BitLen()))
	}
	return ps, nil
}

func composeNets(project, service string) ([]netip.Prefix, error) {
	args := []string{"ps", "-q", "--no-trunc", "--filter", "label=com.docker.compose.project=" + project}
	if service != "" {
		args = append(args, "--filter", "label=com.docker.compose.service="+service)
	}
	out, err := dockerOutput(args...)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	var ps []netip.Prefix
	for _, id := range ids {
		got, err := dockerNets(id)
		if err != nil {
			return nil, err
		}
		ps = append(ps, got...)
	}
	if len(ps) == 0 {
		what := "project " + project
		if service != "" {
			what = "service " + project + "/" + service
		}
		return nil, fmt.Errorf("no addressed running containers for compose %s", what)
	}
	return ps, nil
}

// dockerOutput bounds the call: a wedged daemon must not hang connect,
// which runs this while holding the root lock.
func dockerOutput(args ...string) ([]byte, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return nil, errors.New("docker not found in PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}

func printNets(ps []netip.Prefix) {
	for _, p := range ps {
		fmt.Println(p)
	}
}
