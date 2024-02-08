// Copyright 2019 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
	Overview
		- Tests the hibernation of a  shoot.

	Prerequisites
		- A Shoot exists.

	Test:
		Deploys a default application and hibernates the cluster.
		When the cluster is successfully hibernated it is woken up and the deployed application is tested.
	Expected Output
		- Shoot and deployed app is fully functional after hibernation and wakeup.

	Test:
		Fully reconciles a cluster which means that the default reconciliation as well as the maintenance of
		the shoot is triggered.
	Expected Output
		- Shoot is successfully reconciling.

	Test:
		Rotate kubeconfig for a shoot cluster.
	Expected Output
		- The old kubeconfig to be updated and the old file to be no longer autorized.

	Test:
		Rotate ssh keypair for a shoot cluster.
		Annotate Shoot with "gardener.cloud/operation" = "rotate-ssh-keypair".
	Expected Output
		- Current ssh-keypair should be rotated.
		- Current ssh-keypair should be kept in the system post rotation.

 **/

package operations

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	"github.com/gardener/gardener/pkg/client/kubernetes"
	gardenerutils "github.com/gardener/gardener/pkg/utils/gardener"
	"github.com/gardener/gardener/pkg/utils/secrets"
	"github.com/gardener/gardener/test/framework"
	"github.com/gardener/gardener/test/framework/applications"
)

const (
	hibernationTestTimeout = 1 * time.Hour
	reconcileTimeout       = 40 * time.Minute
)

var _ = ginkgo.Describe("Shoot operation testing", func() {

	f := framework.NewShootFramework(nil)

	f.Default().Serial().CIt("Testing if Shoot can be hibernated successfully", func(ctx context.Context) {
		guestBookTest, err := applications.NewGuestBookTest(f)
		framework.ExpectNoError(err)

		defer guestBookTest.Cleanup(ctx)

		ginkgo.By("Deploy guestbook")
		guestBookTest.DeployGuestBookApp(ctx)
		guestBookTest.Test(ctx)

		ginkgo.By("Hibernate shoot")
		err = f.HibernateShoot(ctx)
		framework.ExpectNoError(err)

		ginkgo.By("Wake up shoot")
		err = f.WakeUpShoot(ctx)
		framework.ExpectNoError(err)

		ginkgo.By("Test guestbook")
		guestBookTest.WaitUntilRedisIsReady(ctx)
		guestBookTest.WaitUntilGuestbookDeploymentIsReady(ctx)
		guestBookTest.Test(ctx)

	}, hibernationTestTimeout)

	f.Default().Serial().CIt("should fully maintain and reconcile a shoot cluster", func(ctx context.Context) {
		ginkgo.By("Maintain shoot")
		err := f.UpdateShoot(ctx, func(shoot *gardencorev1beta1.Shoot) error {
			shoot.Annotations[v1beta1constants.GardenerOperation] = v1beta1constants.ShootOperationMaintain
			return nil
		})
		framework.ExpectNoError(err)

		ginkgo.By("Reconcile shoot")
		err = f.UpdateShoot(ctx, func(shoot *gardencorev1beta1.Shoot) error {
			shoot.Annotations[v1beta1constants.GardenerOperation] = v1beta1constants.GardenerOperationReconcile
			return nil
		})
		framework.ExpectNoError(err)
	}, reconcileTimeout)

	f.Beta().Disruptive().CIt("should rotate the kubeconfig for a shoot cluster", func(ctx context.Context) {
		if !ptr.Deref(f.Shoot.Spec.Kubernetes.EnableStaticTokenKubeconfig, false) {
			ginkgo.Skip("The static token kubeconfig is not enabled for this shoot")
		}

		ginkgo.By("Rotate kubeconfig")
		var (
			secretName = f.Shoot.Name + ".kubeconfig"
		)

		oldKubeconfig, err := framework.GetObjectFromSecret(ctx, f.GardenClient, f.ProjectNamespace, secretName, framework.KubeconfigSecretKeyName)
		framework.ExpectNoError(err)

		oldClient, err := kubernetes.NewClientFromBytes([]byte(oldKubeconfig))
		framework.ExpectNoError(err)
		_, err = oldClient.Kubernetes().Discovery().ServerVersion()
		framework.ExpectNoError(err)

		err = f.UpdateShoot(ctx, func(shoot *gardencorev1beta1.Shoot) error {
			shoot.Annotations[v1beta1constants.GardenerOperation] = v1beta1constants.ShootOperationRotateKubeconfigCredentials
			return nil
		})
		framework.ExpectNoError(err)

		framework.ExpectNoError(f.GetShoot(ctx, f.Shoot))
		gomega.Expect(f.Shoot.Status.Credentials.Rotation.Kubeconfig.LastInitiationTime).NotTo(gomega.BeNil())
		gomega.Expect(f.Shoot.Status.Credentials.Rotation.Kubeconfig.LastCompletionTime).NotTo(gomega.BeNil())

		newKubeconfig, err := framework.GetObjectFromSecret(ctx, f.GardenClient, f.ProjectNamespace, secretName, framework.KubeconfigSecretKeyName)
		framework.ExpectNoError(err)

		// write the updated kubeconfig to the testmachinery shoot kubeconfig path
		defer func() {
			kubeconfigsPath := os.Getenv(framework.TestMachineryKubeconfigsPathEnvVarName)
			if len(kubeconfigsPath) == 0 {
				// do nothing if the environment variable is not defined.
				return
			}
			shootKubeconfigPath := filepath.Join(kubeconfigsPath, "shoot.config")
			framework.ExpectNoError(os.WriteFile(shootKubeconfigPath, []byte(newKubeconfig), os.ModePerm))
		}()

		newClient, err := kubernetes.NewClientFromBytes([]byte(newKubeconfig))
		framework.ExpectNoError(err)
		_, err = newClient.Kubernetes().Discovery().ServerVersion()
		framework.ExpectNoError(err)

		_, err = oldClient.Kubernetes().Discovery().ServerVersion()
		gomega.Expect(err).To(gomega.HaveOccurred())

		gomega.Expect(oldKubeconfig).ToNot(gomega.Equal(newKubeconfig))
	}, reconcileTimeout)

	f.Beta().Serial().CIt("should rotate the ssh keypair for a shoot cluster", func(ctx context.Context) {
		secret := &corev1.Secret{}
		gomega.Expect(f.GardenClient.Client().Get(ctx, client.ObjectKey{Namespace: f.Shoot.Namespace, Name: gardenerutils.ComputeShootProjectResourceName(f.Shoot.Name, gardenerutils.ShootProjectSecretSuffixSSHKeypair)}, secret)).To(gomega.Succeed())
		preRotationPrivateKey := getKeyAndValidate(secret, secrets.DataKeyRSAPrivateKey)
		preRotationPublicKey := getKeyAndValidate(secret, secrets.DataKeySSHAuthorizedKeys)
		err := f.UpdateShoot(ctx, func(s *gardencorev1beta1.Shoot) error {
			metav1.SetMetaDataAnnotation(&s.ObjectMeta, v1beta1constants.GardenerOperation, v1beta1constants.ShootOperationRotateSSHKeypair)
			return nil
		})
		gomega.Expect(err).To(gomega.Succeed())

		gomega.Expect(f.GetShoot(ctx, f.Shoot)).To(gomega.Succeed())
		v, ok := f.Shoot.Annotations[v1beta1constants.GardenerOperation]
		if ok {
			gomega.Expect(v).NotTo(gomega.Equal(v1beta1constants.ShootOperationRotateSSHKeypair))
		}

		gomega.Expect(f.GardenClient.Client().Get(ctx, client.ObjectKey{Namespace: f.Shoot.Namespace, Name: gardenerutils.ComputeShootProjectResourceName(f.Shoot.Name, gardenerutils.ShootProjectSecretSuffixSSHKeypair)}, secret)).To(gomega.Succeed())
		postRotationPrivateKey := getKeyAndValidate(secret, secrets.DataKeyRSAPrivateKey)
		postRotationPublicKey := getKeyAndValidate(secret, secrets.DataKeySSHAuthorizedKeys)

		gomega.Expect(f.GardenClient.Client().Get(ctx, client.ObjectKey{Namespace: f.Shoot.Namespace, Name: gardenerutils.ComputeShootProjectResourceName(f.Shoot.Name, gardenerutils.ShootProjectSecretSuffixOldSSHKeypair)}, secret)).To(gomega.Succeed())
		postRotationOldPrivateKey := getKeyAndValidate(secret, secrets.DataKeyRSAPrivateKey)
		postRotationOldPublicKey := getKeyAndValidate(secret, secrets.DataKeySSHAuthorizedKeys)

		gomega.Expect(preRotationPrivateKey).NotTo(gomega.Equal(postRotationPrivateKey))
		gomega.Expect(preRotationPublicKey).NotTo(gomega.Equal(postRotationPublicKey))
		gomega.Expect(preRotationPrivateKey).To(gomega.Equal(postRotationOldPrivateKey))
		gomega.Expect(preRotationPublicKey).To(gomega.Equal(postRotationOldPublicKey))

	}, reconcileTimeout)
})

func getKeyAndValidate(s *corev1.Secret, field string) []byte {
	v, ok := s.Data[field]
	gomega.Expect(ok).To(gomega.BeTrue())
	gomega.Expect(v).ToNot(gomega.BeEmpty())
	return v
}
