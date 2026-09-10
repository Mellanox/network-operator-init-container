# network-operator-init-container
Init container for NVIDIA Network Operator

## Configuration
The network-operator-init-container container has following required command line arguments:

 - `--configmap-name` name of the configmap with configuration for the app
 - `--configmap-namespace` namespace of the configmap with configuration for the app
 - `--node-name` name of the k8s node on which this app runs

The ConfigMap should include configuration in JSON format:

```
apiVersion: v1
kind: ConfigMap
metadata:
  name: ofed-init-container-config
  namespace: default
data:
  config.json: |-
    {
      "safeDriverLoad": {
        "enable": true,
        "annotation": "some-annotation"
      },
      "updateOfedLabel": {
        "enable": true
      }
    }
```

- `safeDriverLoad` - contains settings related to safeDriverLoad feature
- `safeDriverLoad.enable` - enable safeDriveLoad feature
- `safeDriverLoad.annotation` - annotation to use for safeDriverLoad feature
- `updateOfedLabel` - contains settings related to updateOfedLabel feature
- `updateOfedLabel.enable` - enable updateOfedLabel feature
- `updateOfedLabel.label` - label to use for updateOfedLabel feature


If `safeDriverLoad` feature is enabled then the network-operator-init-container container will set annotation
provided in `safeDriverLoad.annotation` on the Kubernetes Node object identified by `--node-name`.
The container exits with code 0 when the annotation is removed from the Node object.

If `safeDriverLoad` feature is disabled then the container will immediately exit with code 0.

If `updateOfedLabel` feature is enabled then the network-operator-init-container container will set the label
provided in `updateOfedLabel.label` to `"true"` on the Kubernetes Node object identified by `--node-name`.

### Required permissions

```
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: network-operator-init-container
rules:
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list", "patch", "watch", "update"]
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "list"]

```

## Command line arguments

```
NVIDIA Network Operator init container

Usage:
  network-operator-init-container [flags]

Config flags:

      --configmap-key string                                                                                                                                                                          
                key inside the configmap with configuration for the app (default "config.json")
      --configmap-name string                                                                                                                                                                         
                name of the configmap with configuration for the app
      --configmap-namespace string                                                                                                                                                                    
                namespace of the configmap with configuration for the app
      --node-name string                                                                                                                                                                              
                name of the k8s node on which this app runs

Logging flags:

      --log-flush-frequency duration                                                                                                                                                                  
                Maximum number of seconds between log flushes (default 5s)
      --log-json-info-buffer-size quantity                                                                                                                                                            
                [Alpha] In JSON format with split output streams, the info messages can be buffered for a while to increase performance. The default value of zero bytes disables buffering. The size can
                be specified as number of bytes (512), multiples of 1000 (1K), multiples of 1024 (2Ki), or powers of those (3M, 4G, 5Mi, 6Gi). Enable the LoggingAlphaOptions feature gate to use this.
      --log-json-split-stream                                                                                                                                                                         
                [Alpha] In JSON format, write error messages to stderr and info messages to stdout. The default is to write a single stream to stdout. Enable the LoggingAlphaOptions feature gate to use
                this.
      --logging-format string                                                                                                                                                                         
                Sets the log format. Permitted formats: "json" (gated by LoggingBetaOptions), "text". (default "text")
  -v, --v Level                                                                                                                                                                                       
                number for the log level verbosity
      --vmodule pattern=N,...                                                                                                                                                                         
                comma-separated list of pattern=N settings for file-filtered logging (only works for text log format)

General flags:

  -h, --help                                                                                                                                                                                          
                print help and exit
      --version                                                                                                                                                                                       
                print version and exit

Kubernetes flags:

      --kubeconfig string                                                                                                                                                                             
                Paths to a kubeconfig. Only required if out-of-cluster.

```
