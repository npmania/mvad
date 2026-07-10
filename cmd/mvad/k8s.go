package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/npmania/mvad/internal/config"
)

// k8sNets resolves a namespace[/pod-or-selector] entry to pod
// addresses. Only pods on this node count: the source set matches
// forwarded traffic, and other nodes' pods never route through this
// host. Host-network pods are skipped too — their address is the
// host's own.
func k8sNets(entry string) ([]netip.Prefix, error) {
	namespace, sel, _ := strings.Cut(entry, "/")
	args := []string{"get", "pods", "-n", namespace, "-o", "json"}
	switch {
	case sel == "":
	case isPodName(sel):
		args = append(args, "--field-selector", "metadata.name="+sel)
	default:
		args = append(args, "-l", sel)
	}
	out, err := kubectlOutput(args...)
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Spec struct {
				HostNetwork bool `json:"hostNetwork"`
			} `json:"spec"`
			Status struct {
				Phase  string `json:"phase"`
				HostIP string `json:"hostIP"`
				PodIPs []struct {
					IP string `json:"ip"`
				} `json:"podIPs"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("kubectl get pods: %w", err)
	}
	var ps []netip.Prefix
	remote, hostNet := false, false
	for _, pod := range list.Items {
		// Succeeded and Failed release the address for reuse; anything
		// else keeps it — a Pending pod's init containers already
		// send traffic.
		if pod.Status.Phase == "Succeeded" || pod.Status.Phase == "Failed" {
			continue
		}
		node, err := netip.ParseAddr(pod.Status.HostIP)
		if err != nil {
			continue
		}
		if !isLocalAddr(node) {
			remote = true
			continue
		}
		if pod.Spec.HostNetwork {
			hostNet = true
			continue
		}
		for _, pip := range pod.Status.PodIPs {
			a, err := netip.ParseAddr(pip.IP)
			if err != nil {
				continue
			}
			ps = append(ps, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	if len(ps) == 0 {
		switch {
		case remote:
			return nil, errors.New("matching pods are on other nodes; only traffic forwarded through this host can be split")
		case hostNet:
			return nil, errors.New("only host-network pods matched; those share the host's address and follow the host's route")
		default:
			return nil, fmt.Errorf("no addressed pods for %s", entry)
		}
	}
	return ps, nil
}

// A pod name is DNS-1123: lowercase alphanumerics, '-', '.'.
// Anything else marks a label selector.
func isPodName(s string) bool {
	for _, r := range s {
		if !('a' <= r && r <= 'z' || '0' <= r && r <= '9' || r == '-' || r == '.') {
			return false
		}
	}
	return s != ""
}

// kubectlOutput bounds the call: an unreachable apiserver must not
// hang connect, which runs this while holding the root lock.
func kubectlOutput(args ...string) ([]byte, error) {
	bin, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, errors.New("kubectl not found in PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if cfg := callerKubeconfig(); cfg != "" {
		cmd.Env = append(cmd.Env, "KUBECONFIG="+cfg)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}

// callerKubeconfig recovers the invoking user's kubeconfig: sudo and
// pkexec scrub KUBECONFIG and point HOME at root, so kubectl would
// answer from the wrong cluster or none. The caller's environment
// wins, then a config in HOME, then the caller's ~/.kube/config.
func callerKubeconfig() string {
	if os.Getenv("KUBECONFIG") != "" {
		return ""
	}
	if os.Getenv("SUDO_UID") != "" || os.Getenv("PKEXEC_UID") != "" {
		for _, kv := range readInvokerEnv() {
			if v, ok := strings.CutPrefix(kv, "KUBECONFIG="); ok && v != "" {
				return v
			}
		}
	}
	if home := os.Getenv("HOME"); home != "" {
		if _, err := os.Stat(filepath.Join(home, ".kube", "config")); err == nil {
			return ""
		}
	}
	cu, err := config.ResolveCallingUser()
	if err != nil || cu == nil || cu.Home == "" {
		return ""
	}
	p := filepath.Join(cu.Home, ".kube", "config")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}
