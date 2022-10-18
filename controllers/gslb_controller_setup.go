package controllers

/*
Copyright 2022 The k8gb Contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Generated by GoLic, for more details see: https://github.com/AbsaOSS/golic
*/

import (
	"context"
	"fmt"
	"strconv"

	"github.com/k8gb-io/k8gb/controllers/depresolver"

	k8gbv1beta1 "github.com/k8gb-io/k8gb/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	externaldns "sigs.k8s.io/external-dns/endpoint"
)

// SetupWithManager configures controller manager
func (r *GslbReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Figure out Gslb resource name to Reconcile when non controlled Name is updated

	endpointMapHandler := handler.EnqueueRequestsFromMapFunc(
		func(a client.Object) []reconcile.Request {
			gslbList := &k8gbv1beta1.GslbList{}
			opts := []client.ListOption{
				client.InNamespace(a.GetNamespace()),
			}
			c := mgr.GetClient()
			err := c.List(context.TODO(), gslbList, opts...)
			if err != nil {
				log.Info().Msg("Can't fetch gslb objects")
				return nil
			}
			gslbName := ""
			for _, gslb := range gslbList.Items {
				for _, rule := range gslb.Spec.Ingress.Rules {
					for _, path := range rule.HTTP.Paths {
						if path.Backend.Service != nil && path.Backend.Service.Name == a.GetName() {
							gslbName = gslb.Name
						}
					}
				}
			}
			if len(gslbName) > 0 {
				return []reconcile.Request{
					{NamespacedName: types.NamespacedName{
						Name:      gslbName,
						Namespace: a.GetNamespace(),
					}},
				}
			}
			return nil
		})

	ingressMapHandler := handler.EnqueueRequestsFromMapFunc(
		func(a client.Object) []reconcile.Request {
			annotations := a.GetAnnotations()
			if annotationValue, found := annotations[strategyAnnotation]; found {
				c := mgr.GetClient()
				r.createGSLBFromIngress(c, a, annotationValue)
			}
			return nil
		})

	return ctrl.NewControllerManagedBy(mgr).
		For(&k8gbv1beta1.Gslb{}).
		Owns(&netv1.Ingress{}).
		Owns(&externaldns.DNSEndpoint{}).
		Watches(&source.Kind{Type: &corev1.Endpoints{}}, endpointMapHandler).
		Watches(&source.Kind{Type: &netv1.Ingress{}}, ingressMapHandler).
		Complete(r)
}

func (r *GslbReconciler) createGSLBFromIngress(c client.Client, a client.Object, strategy string) {
	log.Info().
		Str("annotation", fmt.Sprintf("(%s:%s)", strategyAnnotation, strategy)).
		Str("ingress", a.GetName()).
		Msg("Detected strategy annotation on ingress")

	ingressToReuse := &netv1.Ingress{}
	err := c.Get(context.Background(), client.ObjectKey{
		Namespace: a.GetNamespace(),
		Name:      a.GetName(),
	}, ingressToReuse)
	if err != nil {
		log.Info().
			Str("ingress", a.GetName()).
			Msg("Ingress does not exist anymore. Skipping Glsb creation...")
		return
	}
	gslbExist := &k8gbv1beta1.Gslb{}
	err = c.Get(context.Background(), client.ObjectKey{
		Namespace: a.GetNamespace(),
		Name:      a.GetName(),
	}, gslbExist)
	if err == nil {
		log.Info().
			Str("gslb", gslbExist.Name).
			Msg("Gslb already exists. Skipping Gslb creation...")
		return
	}
	gslb := &k8gbv1beta1.Gslb{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   a.GetNamespace(),
			Name:        a.GetName(),
			Annotations: a.GetAnnotations(),
		},
		Spec: k8gbv1beta1.GslbSpec{
			Ingress: k8gbv1beta1.FromV1IngressSpec(ingressToReuse.Spec),
		},
	}

	gslb.Spec.Strategy, err = r.parseStrategy(a.GetAnnotations(), strategy)
	if err != nil {
		log.Err(err).
			Str("gslb", gslbExist.Name).
			Msg("can't parse Gslb strategy")
		return
	}

	err = controllerutil.SetControllerReference(ingressToReuse, gslb, r.Scheme)
	if err != nil {
		log.Err(err).
			Str("ingress", ingressToReuse.Name).
			Str("gslb", gslb.Name).
			Msg("Cannot set the Ingress as the owner of the Gslb")
	}

	log.Info().
		Str("gslb", gslb.Name).
		Msg(fmt.Sprintf("Creating a new Gslb out of Ingress with '%s' annotation", strategyAnnotation))
	err = c.Create(context.Background(), gslb)
	if err != nil {
		log.Err(err).Msg("Glsb creation failed")
	}
}

func (r *GslbReconciler) parseStrategy(annotations map[string]string, strategy string) (result k8gbv1beta1.Strategy, err error) {
	toInt := func(k string, v string) (int, error) {
		intValue, err := strconv.Atoi(v)
		if err != nil {
			return -1, fmt.Errorf("can't parse annotation value %s to int for key %s", v, k)
		}
		return intValue, nil
	}

	result = k8gbv1beta1.Strategy{
		Type: strategy,
	}

	for annotationKey, annotationValue := range annotations {
		switch annotationKey {
		case dnsTTLSecondsAnnotation:
			if result.DNSTtlSeconds, err = toInt(annotationKey, annotationValue); err != nil {
				return result, err
			}
		case splitBrainThresholdSecondsAnnotation:
			if result.SplitBrainThresholdSeconds, err = toInt(annotationKey, annotationValue); err != nil {
				return result, err
			}
		case primaryGeoTagAnnotation:
			result.PrimaryGeoTag = annotationValue
		}
	}

	if strategy == depresolver.FailoverStrategy {
		if len(result.PrimaryGeoTag) == 0 {
			return result, fmt.Errorf("%s strategy requires annotation %s", depresolver.FailoverStrategy, primaryGeoTagAnnotation)
		}
	}

	return result, nil
}