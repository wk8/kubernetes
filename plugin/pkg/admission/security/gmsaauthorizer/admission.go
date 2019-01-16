/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package gmsaauthorizer contains an admission controller that enables support for
// Microsoft's group Managed Service Accounts (a.k.a gMSA - more doc at
// https://docs.microsoft.com/en-us/windows-server/security/group-managed-service-accounts/group-managed-service-accounts-overview)
package gmsaauthorizer

import (
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	genericadmissioninit "k8s.io/apiserver/pkg/admission/initializer"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
	api "k8s.io/kubernetes/pkg/apis/core"
	"k8s.io/kubernetes/pkg/serviceaccount"
)

const (
	// PluginName indicates name of admission plugin.
	PluginName = "GMSAAuthorizer"

	// For the alpha version of gMSA support, we use annotations rather than an API field
	// gMSAConfigMapAnnotationKey is set by users, and should give the name of the configmap
	// containing the gMSA's cred spec
	gMSAConfigMapAnnotationKey = "pod.alpha.kubernetes.io/windows-gmsa-config-map"
	// gMSACredSpecAnnotationKey's use is restricted to this admission controller, and any
	// attempt to set a value by the user will result in an error
	gMSACredSpecAnnotationKey = "pod.alpha.kubernetes.io/windows-gmsa-cred-spec"
)

// Register registers a plugin
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(config io.Reader) (admission.Interface, error) {
		return newGMSAAuthorizer(), nil
	})
}

func newGMSAAuthorizer() *GMSAAuthorizer {
	return &GMSAAuthorizer{
		Handler: admission.NewHandler(admission.Create, admission.Update),
	}
}

// A GMSAAuthorizer looks at all new pods and, if they try to make use of a gMSA, check that
// their spec's serviceAccountName are authorized for the `use`
// verb on the gMSA's configmap.
// TODO wkpo comment?
type GMSAAuthorizer struct {
	*admission.Handler
	authorizer authorizer.Authorizer
	client     kubernetes.Interface
}

// have the compiler check that we satisfy the right interfaces
var _ admission.MutationInterface = &GMSAAuthorizer{}
var _ genericadmissioninit.WantsAuthorizer = &GMSAAuthorizer{}
var _ genericadmissioninit.WantsExternalKubeClientSet = &GMSAAuthorizer{}

// SetAuthorizer sets the authorizer.
func (a *GMSAAuthorizer) SetAuthorizer(authorizer authorizer.Authorizer) {
	a.authorizer = authorizer
}

// SetExternalKubeClientSet implements the WantsInternalKubeClientSet interface.
func (a *GMSAAuthorizer) SetExternalKubeClientSet(client kubernetes.Interface) {
	a.client = client
}

// ValidateInitialization ensures an authorizer and a client have both been provided.
func (a *GMSAAuthorizer) ValidateInitialization() error {
	if a.authorizer == nil {
		return fmt.Errorf("%s requires an authorizer", PluginName)
	}
	if a.client == nil {
		return fmt.Errorf("%s requires an internal kube client", PluginName)
	}
	return nil
}

var notAPodError = apierrors.NewBadRequest("Resource was marked with kind Pod but was unable to be converted")

// Admit makes sure that pods using gMSA's are created using ServiceAccounts who are indeed
// authorized to use the requested gMSA, and inlines it into the pod's spec
func (a *GMSAAuthorizer) Admit(attributes admission.Attributes) error {
	if !isPodRequest(attributes) {
		return nil
	}

	pod, ok := attributes.GetObject().(*api.Pod)
	if !ok {
		return notAPodError
	}

	// forbid updates
	// TODO wkpo: what happens then? restart loop? error message?
	if attributes.GetOperation() == admission.Update {
		return ensureNoUpdates(attributes, pod)
	}

	// only this plugin is allowed to populate the actual contents of the cred spec
	if _, present := pod.Annotations[gMSACredSpecAnnotationKey]; present {
		return apierrors.NewBadRequest(fmt.Sprintf("Forbidden to set the %s annotation", gMSACredSpecAnnotationKey))
	}

	configMapName, present := pod.Annotations[gMSAConfigMapAnnotationKey]
	if !present || configMapName == "" {
		// nothing to do then
		return nil
	}

	// let's check that the associated service account can read the relevant service map
	if !a.isAuthorizedToReadConfigMap(attributes, pod, configMapName) {
		return apierrors.NewForbidden(schema.GroupResource{Resource: string(corev1.ResourceConfigMaps)},
			configMapName,
			fmt.Errorf(fmt.Sprintf("service account %s does not have access to the config map at %s", pod.Spec.ServiceAccountName, configMapName)))
	}

	// finally inline the config map's contents into the spec
	configMap, err := a.client.CoreV1().ConfigMaps(attributes.GetNamespace()).Get(configMapName, metav1.GetOptions{})
	if err != nil && apierrors.IsNotFound(err) || configMap == nil {
		return apierrors.NewBadRequest(fmt.Sprintf("config map %s does not exist", configMapName))
	} else if err != nil {
		return apierrors.NewInternalError(err)
	}
	credSpecBytes, err := configMap.Marshal()
	if err != nil {
		return apierrors.NewInternalError(err)
	}
	pod.Annotations[gMSACredSpecAnnotationKey] = string(credSpecBytes)

	return nil
}

// isPodRequest checks that the request involves a pod
func isPodRequest(attributes admission.Attributes) bool {
	return len(attributes.GetSubresource()) == 0 && attributes.GetResource().GroupResource() == api.Resource("pods")
}

// ensureNoUpdates ensures that there are no updates to any of the 2 annotations we care about
func ensureNoUpdates(attributes admission.Attributes, pod *api.Pod) error {
	oldPod, ok := attributes.GetOldObject().(*api.Pod)
	if !ok {
		return notAPodError
	}

	if pod.Annotations[gMSAConfigMapAnnotationKey] != oldPod.Annotations[gMSAConfigMapAnnotationKey] ||
		pod.Annotations[gMSACredSpecAnnotationKey] != oldPod.Annotations[gMSACredSpecAnnotationKey] {
		return apierrors.NewBadRequest("Cannot update an existing pod's gMSA annotation")
	}

	return nil
}

// TODO wkpo: read? use?
// isAuthorizedToReadConfigMap checks that the service account is authorized to read that config map
func (a *GMSAAuthorizer) isAuthorizedToReadConfigMap(attributes admission.Attributes, pod *api.Pod, configMapName string) bool {
	var serviceAccountInfo user.Info
	if pod.Spec.ServiceAccountName != "" {
		// TODO wkpo: what happens if there's no service account in the spec? should we deny then?
		serviceAccountInfo = serviceaccount.UserInfo(attributes.GetNamespace(), pod.Spec.ServiceAccountName, "")
	}

	authorizerAttributes := authorizer.AttributesRecord{
		User:      serviceAccountInfo,
		Verb:      "use",
		Namespace: attributes.GetNamespace(),

		// TODO wkpo c bon ca?
		// APIGroup:        attributes.GetResource().Group
		Resource: string(corev1.ResourceConfigMaps), // TODO wkpo ? "configmap",
		Name:     configMapName,

		ResourceRequest: true,
	}

	decision, reason, err := a.authorizer.Authorize(authorizerAttributes)
	if err != nil {
		klog.V(5).Infof("cannot authorize for policy: %v,%v", reason, err)
	}
	return decision == authorizer.DecisionAllow
}
