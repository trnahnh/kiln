package controller

import (
	"crypto/rand"
	"fmt"
	"math/big"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	platformv1 "github.com/trnahnh/kiln/operator/api/v1"
)

const (
	finalizerName       = "platform.internal/tenantdatabase"
	labelTenantDatabase = "platform.internal/tenantdatabase"
	labelOperation      = "platform.internal/operation"
	annotationBackupID  = "platform.internal/backup-id"

	operationBackup  = "backup"
	operationRestore = "restore"

	postgresPort     = 5432
	postgresDataPath = "/var/lib/postgresql/data"
	passwordKey      = "POSTGRES_PASSWORD"
)

func secretName(tdb *platformv1.TenantDatabase) string      { return tdb.Name + "-credentials" }
func dataPVCName(tdb *platformv1.TenantDatabase) string     { return tdb.Name + "-data" }
func backupsPVCName(tdb *platformv1.TenantDatabase) string  { return tdb.Name + "-backups" }
func serviceName(tdb *platformv1.TenantDatabase) string     { return tdb.Name }
func statefulSetName(tdb *platformv1.TenantDatabase) string { return tdb.Name }

func instanceLabels(tdb *platformv1.TenantDatabase) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "tenantdatabase",
		"app.kubernetes.io/instance": tdb.Name,
		labelTenantDatabase:          tdb.Name,
	}
}

func storageQuantity(gb int32) resource.Quantity {
	return resource.MustParse(fmt.Sprintf("%dGi", gb))
}

func postgresImage(tdb *platformv1.TenantDatabase) string {
	return "postgres:" + tdb.Spec.Version
}

func desiredSecret(tdb *platformv1.TenantDatabase) (*corev1.Secret, error) {
	password, err := randomPassword(32)
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName(tdb), Namespace: tdb.Namespace, Labels: instanceLabels(tdb)},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{passwordKey: password},
	}, nil
}

func randomPassword(n int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	out := make([]byte, n)
	for i := range out {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		out[i] = alphabet[idx.Int64()]
	}
	return string(out), nil
}

func desiredPVC(tdb *platformv1.TenantDatabase, name string, gb int32) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tdb.Namespace, Labels: instanceLabels(tdb)},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: storageQuantity(gb)},
			},
		},
	}
}

func desiredService(tdb *platformv1.TenantDatabase) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: serviceName(tdb), Namespace: tdb.Namespace, Labels: instanceLabels(tdb)},
		Spec: corev1.ServiceSpec{
			Selector: instanceLabels(tdb),
			Ports:    []corev1.ServicePort{{Name: "postgres", Port: postgresPort}},
		},
	}
}

// The data volume is a PVC the operator owns rather than a volumeClaimTemplate: templates
// cannot be resized in place and their claims are not garbage-collected with the owner.
func desiredStatefulSet(tdb *platformv1.TenantDatabase) *appsv1.StatefulSet {
	labels := instanceLabels(tdb)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: statefulSetName(tdb), Namespace: tdb.Namespace, Labels: labels},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    ptr.To[int32](1),
			ServiceName: serviceName(tdb),
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "postgres",
						Image: postgresImage(tdb),
						Ports: []corev1.ContainerPort{{Name: "postgres", ContainerPort: postgresPort}},
						Env: []corev1.EnvVar{
							{Name: "PGDATA", Value: postgresDataPath + "/pgdata"},
							passwordEnv(tdb),
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: postgresDataPath}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								Exec: &corev1.ExecAction{Command: []string{"pg_isready", "-U", "postgres"}},
							},
							PeriodSeconds: 5,
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: dataPVCName(tdb)},
						},
					}},
				},
			},
		},
	}
}

func passwordEnv(tdb *platformv1.TenantDatabase) corev1.EnvVar {
	return corev1.EnvVar{
		Name: passwordKey,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName(tdb)},
				Key:                  passwordKey,
			},
		},
	}
}

func desiredJob(tdb *platformv1.TenantDatabase, operation, backupID, script string) *batchv1.Job {
	labels := instanceLabels(tdb)
	labels[labelOperation] = operation
	pgPassword := passwordEnv(tdb)
	pgPassword.Name = "PGPASSWORD"
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-%s-%s", tdb.Name, operation, backupIDForName(backupID)),
			Namespace:   tdb.Namespace,
			Labels:      labels,
			Annotations: map[string]string{annotationBackupID: backupID},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To[int32](0),
			TTLSecondsAfterFinished: ptr.To[int32](24 * 3600),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    operation,
						Image:   postgresImage(tdb),
						Command: []string{"/bin/sh", "-c"},
						Args:    []string{script},
						Env: []corev1.EnvVar{
							{Name: "PGHOST", Value: serviceName(tdb)},
							{Name: "PGUSER", Value: "postgres"},
							{Name: "PGDATABASE", Value: "postgres"},
							pgPassword,
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "backups", MountPath: backupsMountPath}},
					}},
					Volumes: []corev1.Volume{{
						Name: "backups",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: backupsPVCName(tdb)},
						},
					}},
				},
			},
		},
	}
}

// Job names must be DNS labels; backup IDs are upper-case timestamps or "latest".
func backupIDForName(backupID string) string {
	out := make([]byte, 0, len(backupID))
	for i := 0; i < len(backupID); i++ {
		c := backupID[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			out = append(out, c)
		}
	}
	return string(out)
}

func jobFinished(job *batchv1.Job) bool {
	return jobSucceeded(job) || jobFailed(job)
}

func jobSucceeded(job *batchv1.Job) bool {
	return hasJobCondition(job, batchv1.JobComplete)
}

func jobFailed(job *batchv1.Job) bool {
	return hasJobCondition(job, batchv1.JobFailed)
}

func hasJobCondition(job *batchv1.Job, t batchv1.JobConditionType) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == t && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
