module github.com/trnahnh/kiln/scheduler-plugin

go 1.26.0

// k8s.io/kubernetes pins its staging modules by relative path; consumers must pin the matching tags.
replace (
	k8s.io/api => k8s.io/api v0.36.4
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.36.4
	k8s.io/apimachinery => k8s.io/apimachinery v0.36.4
	k8s.io/apiserver => k8s.io/apiserver v0.36.4
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.36.4
	k8s.io/client-go => k8s.io/client-go v0.36.4
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.36.4
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.36.4
	k8s.io/code-generator => k8s.io/code-generator v0.36.4
	k8s.io/component-base => k8s.io/component-base v0.36.4
	k8s.io/component-helpers => k8s.io/component-helpers v0.36.4
	k8s.io/controller-manager => k8s.io/controller-manager v0.36.4
	k8s.io/cri-api => k8s.io/cri-api v0.36.4
	k8s.io/cri-client => k8s.io/cri-client v0.36.4
	k8s.io/cri-streaming => k8s.io/cri-streaming v0.36.4
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.36.4
	k8s.io/dynamic-resource-allocation => k8s.io/dynamic-resource-allocation v0.36.4
	k8s.io/endpointslice => k8s.io/endpointslice v0.36.4
	k8s.io/externaljwt => k8s.io/externaljwt v0.36.4
	k8s.io/kms => k8s.io/kms v0.36.4
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.36.4
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.36.4
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.36.4
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.36.4
	k8s.io/kubectl => k8s.io/kubectl v0.36.4
	k8s.io/kubelet => k8s.io/kubelet v0.36.4
	k8s.io/metrics => k8s.io/metrics v0.36.4
	k8s.io/mount-utils => k8s.io/mount-utils v0.36.4
	k8s.io/pod-security-admission => k8s.io/pod-security-admission v0.36.4
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.36.4
	k8s.io/sample-cli-plugin => k8s.io/sample-cli-plugin v0.36.4
	k8s.io/sample-controller => k8s.io/sample-controller v0.36.4
	k8s.io/streaming => k8s.io/streaming v0.36.4
)

require (
	github.com/aws/aws-sdk-go-v2 v1.46.0
	github.com/aws/aws-sdk-go-v2/config v1.33.3
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.329.0
	k8s.io/api v0.36.4
	k8s.io/apimachinery v0.36.4
)

require (
	github.com/aws/aws-sdk-go-v2/credentials v1.20.3 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.19.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.9.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.37.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.42.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.49.0 // indirect
	github.com/aws/smithy-go v1.28.1 // indirect
	github.com/fxamacker/cbor/v2 v2.9.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.yaml.in/yaml/v2 v2.4.3 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260317180543-43fb72c5454a // indirect
	k8s.io/utils v0.0.0-20260210185600-b8788abfbbc2 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.3.3 // indirect
)
