package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/adapters/kubernetesdiscovery"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domainstore"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kube"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kubeops"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/privateinventory"
)

func newPrivateInventoryService(store *domainstore.Store) (*privateinventory.Service, error) {
	runtime := &privateKubernetesRuntime{discoverers: make(map[domain.RecordRef]*kubernetesdiscovery.Discoverer)}
	return privateinventory.New(store, privateinventory.WithKubernetesDiscover(runtime.discover))
}

type privateKubernetesRuntime struct {
	mu          sync.Mutex
	discoverers map[domain.RecordRef]*kubernetesdiscovery.Discoverer
}

func (runtimeState *privateKubernetesRuntime) discover(ctx context.Context, environment domain.Environment, connection domainstore.Connection, request privateinventory.DiscoverRequest) (*domain.InventorySnapshot, string, error) {
	config, err := privateKubeConfig(connection.Endpoint)
	if err != nil {
		return nil, "", errors.New("load registered Kubernetes configuration")
	}
	config, observed, err := pinPrivateKubeConfig(config)
	if err != nil {
		return nil, "", err
	}
	if subtle.ConstantTimeCompare([]byte(observed), []byte(connection.ExpectedClusterFingerprint)) != 1 {
		return nil, "", errors.New("registered Kubernetes API/CA identity changed")
	}

	runtimeState.mu.Lock()
	discoverer := runtimeState.discoverers[environment.Reference()]
	runtimeState.mu.Unlock()
	if discoverer == nil {
		configured, configureErr := newPrivateKubernetesDiscoverer(config, environment, connection)
		if configureErr != nil {
			return nil, "", configureErr
		}
		runtimeState.mu.Lock()
		if existing := runtimeState.discoverers[environment.Reference()]; existing != nil {
			discoverer = existing
		} else {
			runtimeState.discoverers[environment.Reference()] = configured
			discoverer = configured
		}
		runtimeState.mu.Unlock()
	}
	result, err := discoverer.Discover(ctx, environment, kubernetesdiscovery.DiscoverRequest{
		PrincipalScope: environment.TenantScope, Namespaces: request.Namespaces,
		ResourceKinds: request.ResourceKinds, IncludeNodes: request.IncludeNodes, CursorHandle: request.CursorHandle,
	})
	if err != nil {
		return nil, "", err
	}
	return result.Snapshot, result.CursorHandle, nil
}

func newPrivateKubernetesDiscoverer(config *rest.Config, environment domain.Environment, connection domainstore.Connection) (*kubernetesdiscovery.Discoverer, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, errors.New("register Kubernetes discovery scheme")
	}
	kubeClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, errors.New("create registered Kubernetes client")
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, errors.New("create registered Kubernetes clientset")
	}
	metadataClient, err := metadata.NewForConfig(config)
	if err != nil {
		return nil, errors.New("create registered Kubernetes metadata client")
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, errors.New("create registered Kubernetes dynamic client")
	}
	reader, err := kubeops.New(kubeClient, clientset.Discovery(), metadataClient, dynamicClient, diagnostics.NewKubernetesLogReader(clientset), config.Host)
	if err != nil {
		return nil, errors.New("configure registered Kubernetes reader")
	}
	return kubernetesdiscovery.NewDiscoverer(reader, connection.ClusterID, environment.Spec.IdentityFingerprint)
}

func privateKubeConfig(endpoint string) (*rest.Config, error) {
	if endpoint == "existing-default" {
		return kube.Config("")
	}
	return kube.Config(endpoint)
}

func observedClusterFingerprint(config *rest.Config) (string, error) {
	if config == nil || strings.TrimSpace(config.Host) == "" || config.TLSClientConfig.Insecure {
		return "", errors.New("Kubernetes API endpoint and verified CA are required")
	}
	ca := append([]byte(nil), config.TLSClientConfig.CAData...)
	if len(ca) == 0 && config.TLSClientConfig.CAFile != "" {
		file, err := os.Open(config.TLSClientConfig.CAFile)
		if err != nil {
			return "", errors.New("read Kubernetes CA file")
		}
		defer file.Close()
		ca, err = io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil || len(ca) > 1<<20 {
			return "", errors.New("Kubernetes CA file is invalid or too large")
		}
	}
	if len(ca) == 0 {
		return "", errors.New("Kubernetes CA data is required for identity verification")
	}
	caDigest := sha256.Sum256(ca)
	identity := struct {
		APIServer string `json:"apiServer"`
		CASHA256  string `json:"caSHA256"`
	}{strings.TrimSpace(config.Host), hex.EncodeToString(caDigest[:])}
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", errors.New("encode Kubernetes identity")
	}
	canonical, err := domain.CanonicalizeJSON(raw, domain.CanonicalOptions{})
	if err != nil {
		return "", errors.New("canonicalize Kubernetes identity")
	}
	sum := sha256.Sum256(canonical)
	return domain.DigestPrefix + hex.EncodeToString(sum[:]), nil
}

func pinPrivateKubeConfig(config *rest.Config) (*rest.Config, string, error) {
	if config == nil {
		return nil, "", errors.New("Kubernetes configuration is unavailable")
	}
	pinned := rest.CopyConfig(config)
	ca := append([]byte(nil), pinned.TLSClientConfig.CAData...)
	if len(ca) == 0 && pinned.TLSClientConfig.CAFile != "" {
		file, err := os.Open(pinned.TLSClientConfig.CAFile)
		if err != nil {
			return nil, "", errors.New("read registered Kubernetes CA")
		}
		defer file.Close()
		ca, err = io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil || len(ca) > 1<<20 {
			return nil, "", errors.New("registered Kubernetes CA is invalid or too large")
		}
	}
	pinned.TLSClientConfig.CAData = ca
	pinned.TLSClientConfig.CAFile = ""
	fingerprint, err := observedClusterFingerprint(pinned)
	if err != nil {
		return nil, "", err
	}
	return pinned, fingerprint, nil
}
