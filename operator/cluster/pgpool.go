package cluster

/*
 Copyright 2017-2018 Crunchy Data Solutions, Inc.
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

import (
	"bytes"
	"encoding/json"
	log "github.com/Sirupsen/logrus"
	crv1 "github.com/crunchydata/postgres-operator/apis/cr/v1"
	"github.com/crunchydata/postgres-operator/kubeapi"
	"github.com/crunchydata/postgres-operator/operator"
	"github.com/crunchydata/postgres-operator/util"
	"k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"os"
	"time"
)

type PgpoolPasswdFields struct {
	Username string
	Password string
}

type PgpoolHBAFields struct {
}

type PgpoolConfFields struct {
	PG_PRIMARY_SERVICE_NAME string
	PG_REPLICA_SERVICE_NAME string
	PG_USERNAME             string
	PG_PASSWORD             string
}

type PgpoolTemplateFields struct {
	Name               string
	ClusterName        string
	SecretsName        string
	CCPImagePrefix     string
	CCPImageTag        string
	ContainerResources string
	Port               string
	PrimaryServiceName string
	ReplicaServiceName string
}

const PGPOOL_SUFFIX = "-pgpool"

func ReconfigurePgpoolFromTask(clientset *kubernetes.Clientset, restclient *rest.RESTClient, task *crv1.Pgtask, namespace string) {
	log.Debug("ReconfigurePgpoolFromTask task cluster=[%s]\n", task.Spec.Parameters[util.LABEL_PGPOOL_TASK_CLUSTER])

	//look up the pgcluster from the task
	clusterName := task.Spec.Parameters[util.LABEL_PGPOOL_TASK_CLUSTER]
	pgcluster := crv1.Pgcluster{}

	found, err := kubeapi.Getpgcluster(restclient, &pgcluster, clusterName, namespace)
	if !found || err != nil {
		log.Error(err)
		return
	}

	depName := clusterName + "-pgpool"
	//remove the pgpool deployment (deployment name is the same as svcname)
	err = kubeapi.DeleteDeployment(clientset, depName, namespace)
	if err != nil {
		log.Error(err)
	}

	//remove the pgpool secret
	secretName := clusterName + "-pgpool-secret"
	err = kubeapi.DeleteSecret(clientset, secretName, namespace)
	if err != nil {
		log.Error(err)
	}

	//check for the deployment to be fully deleted
	for i := 0; i < 10; i++ {
		_, found, err := kubeapi.GetDeployment(clientset, depName, namespace)
		if !found {
			break
		}
		if err != nil {
			log.Error(err)
		}
		log.Debugf("pgpool reconfigure sleeping till deployment [%s] is removed\n", depName)
		time.Sleep(time.Second * time.Duration(4))
	}

	//create the pgpool but leave the existing service in place
	AddPgpool(clientset, &pgcluster, namespace, false)

	//remove task to cleanup
	err = kubeapi.Deletepgtask(restclient, task.Spec.Name, namespace)
	if err != nil {
		log.Error(err)
		return
	}

	log.Debugf("reconfigure pgpool to cluster [%s]\n", clusterName)
}

func AddPgpoolFromTask(clientset *kubernetes.Clientset, restclient *rest.RESTClient, task *crv1.Pgtask, namespace string) {
	log.Debug("AddPgpoolFromTask task cluster=[%s]\n", task.Spec.Parameters[util.LABEL_PGPOOL_TASK_CLUSTER])

	//look up the pgcluster from the task
	clusterName := task.Spec.Parameters[util.LABEL_PGPOOL_TASK_CLUSTER]
	pgcluster := crv1.Pgcluster{}

	found, err := kubeapi.Getpgcluster(restclient, &pgcluster, clusterName, namespace)
	if !found || err != nil {
		log.Error(err)
		return
	}
	AddPgpool(clientset, &pgcluster, namespace, true)

	//remove task
	err = kubeapi.Deletepgtask(restclient, task.Spec.Name, namespace)
	if err != nil {
		log.Error(err)
		return
	}

	//update the pgcluster CRD
	pgcluster.Spec.UserLabels[util.LABEL_PGPOOL] = "true"
	err = kubeapi.Updatepgcluster(restclient, &pgcluster, pgcluster.Name, namespace)
	if err != nil {
		log.Error(err)
		return
	}
	log.Debugf("added pgpool to cluster [%s]\n", clusterName)
}

func DeletePgpoolFromTask(clientset *kubernetes.Clientset, restclient *rest.RESTClient, task *crv1.Pgtask, namespace string) {
	log.Debug("DeletePgpoolFromTask task cluster=[%s]\n", task.Spec.Parameters[util.LABEL_PGPOOL_TASK_CLUSTER])

	//look up the pgcluster from the task
	clusterName := task.Spec.Parameters[util.LABEL_PGPOOL_TASK_CLUSTER]
	pgcluster := crv1.Pgcluster{}

	found, err := kubeapi.Getpgcluster(restclient, &pgcluster, clusterName, namespace)
	if !found || err != nil {
		log.Error(err)
		return
	}

	//remove the pgpool service
	serviceName := clusterName + "-pgpool"
	err = kubeapi.DeleteService(clientset, serviceName, namespace)
	if err != nil {
		log.Error(err)
	}

	//remove the pgpool deployment (deployment name is the same as svcname)
	err = kubeapi.DeleteDeployment(clientset, serviceName, namespace)
	if err != nil {
		log.Error(err)
	}

	//remove the pgpool secret
	secretName := clusterName + "-pgpool-secret"
	err = kubeapi.DeleteSecret(clientset, secretName, namespace)
	if err != nil {
		log.Error(err)
	}

	//remove task
	err = kubeapi.Deletepgtask(restclient, task.Spec.Name, namespace)
	if err != nil {
		log.Error(err)
	}

	//update the pgcluster CRD
	pgcluster.Spec.UserLabels[util.LABEL_PGPOOL] = "false"
	err = kubeapi.Updatepgcluster(restclient, &pgcluster, pgcluster.Name, namespace)
	if err != nil {
		log.Error(err)
	}
	log.Debugf("delete pgpool from cluster [%s]\n", clusterName)
}

// ProcessPgpool ...
func AddPgpool(clientset *kubernetes.Clientset, cl *crv1.Pgcluster, namespace string, createService bool) {
	var doc bytes.Buffer
	var err error

	//generate a secret for pgpool using the testuser credential
	secretName := cl.Spec.Name + "-" + util.LABEL_PGPOOL_SECRET
	primaryName := cl.Spec.Name
	replicaName := cl.Spec.Name + "-replica"
	err = CreatePgpoolSecret(clientset, primaryName, replicaName, primaryName, secretName, namespace)
	if err != nil {
		log.Error(err)
		return
	}
	log.Debug("pgpool secret created")

	clusterName := cl.Spec.Name
	pgpoolName := clusterName + PGPOOL_SUFFIX
	log.Debug("adding a pgpool " + pgpoolName)

	//create the pgpool deployment
	fields := PgpoolTemplateFields{
		Name:           pgpoolName,
		ClusterName:    clusterName,
		CCPImagePrefix: operator.Pgo.Cluster.CCPImagePrefix,
		CCPImageTag:    cl.Spec.CCPImageTag,
		Port:           "5432",
		SecretsName:    secretName,
	}

	err = operator.PgpoolTemplate.Execute(&doc, fields)
	if err != nil {
		log.Error(err)
		return
	}

	if operator.CRUNCHY_DEBUG {
		operator.PgpoolTemplate.Execute(os.Stdout, fields)
	}

	deployment := v1beta1.Deployment{}
	err = json.Unmarshal(doc.Bytes(), &deployment)
	if err != nil {
		log.Error("error unmarshalling pgpool json into Deployment " + err.Error())
		return
	}

	err = kubeapi.CreateDeployment(clientset, &deployment, namespace)
	if err != nil {
		log.Error("error creating pgpool Deployment " + err.Error())
		return
	}

	if createService {
		//create a service for the pgpool
		svcFields := ServiceTemplateFields{}
		svcFields.Name = pgpoolName
		svcFields.ClusterName = clusterName
		svcFields.Port = "5432"

		err = CreateService(clientset, &svcFields, namespace)
		if err != nil {
			log.Error(err)
			return
		}
	}
}

// DeletePgpool
func DeletePgpool(clientset *kubernetes.Clientset, clusterName, namespace string) {

	pgpoolDepName := clusterName + "-pgpool"

	kubeapi.DeleteDeployment(clientset, pgpoolDepName, namespace)

	//delete the service name=<clustename>-pgpool

	kubeapi.DeleteService(clientset, pgpoolDepName, namespace)

}

// CreatePgpoolSecret create a secret used by pgpool
func CreatePgpoolSecret(clientset *kubernetes.Clientset, primary, replica, db, secretName, namespace string) error {

	var err error
	var username, password string
	var pgpoolHBABytes, pgpoolConfBytes, pgpoolPasswdBytes []byte

	pgpoolHBABytes, err = getPgpoolHBA()
	if err != nil {
		log.Error(err)
		return err
	}

	pgpoolPasswdBytes, username, password, err = getPgpoolPasswd(clientset, namespace, db)
	if err != nil {
		log.Error(err)
		return err
	}

	pgpoolConfBytes, err = getPgpoolConf(primary, replica, username, password)
	if err != nil {
		log.Error(err)
		return err
	}

	secret := v1.Secret{}

	secret.Name = secretName
	secret.ObjectMeta.Labels = make(map[string]string)
	secret.ObjectMeta.Labels[util.LABEL_PG_DATABASE] = db
	secret.ObjectMeta.Labels[util.LABEL_PGPOOL] = "true"
	secret.Data = make(map[string][]byte)
	secret.Data["pgpool.conf"] = pgpoolConfBytes
	secret.Data["pool_hba.conf"] = pgpoolHBABytes
	secret.Data["pool_passwd"] = pgpoolPasswdBytes

	err = kubeapi.CreateSecret(clientset, &secret, namespace)

	return err

}

func getPgpoolHBA() ([]byte, error) {
	var err error

	fields := PgpoolHBAFields{}

	var doc bytes.Buffer
	err = operator.PgpoolHBATemplate.Execute(&doc, fields)
	if err != nil {
		log.Error(err)
		return doc.Bytes(), err
	}
	log.Debug(doc.String())

	return doc.Bytes(), err
}

func getPgpoolConf(primary, replica, username, password string) ([]byte, error) {
	var err error

	fields := PgpoolConfFields{}
	fields.PG_PRIMARY_SERVICE_NAME = primary
	fields.PG_REPLICA_SERVICE_NAME = replica
	fields.PG_USERNAME = username
	fields.PG_PASSWORD = password

	var doc bytes.Buffer
	err = operator.PgpoolConfTemplate.Execute(&doc, fields)
	if err != nil {
		log.Error(err)
		return doc.Bytes(), err
	}
	log.Debug(doc.String())

	return doc.Bytes(), err
}

func getPgpoolPasswd(clientset *kubernetes.Clientset, namespace, clusterName string) ([]byte, string, string, error) {
	var doc bytes.Buffer
	var pgpoolUsername, pgpoolPassword string

	//go get all non-pgpool secrets
	selector := util.LABEL_PG_DATABASE + "=" + clusterName + "," + util.LABEL_PGPOOL + "!=true"
	secrets, err := kubeapi.GetSecrets(clientset, selector, namespace)
	if err != nil {
		log.Error(err)
		return doc.Bytes(), pgpoolUsername, pgpoolPassword, err
	}

	creds := make([]PgpoolPasswdFields, 0)
	for _, sec := range secrets.Items {
		//log.Debugf("in pgpool passwd with username=%s password=%s\n", sec.Data[util.LABEL_USERNAME][:], sec.Data[util.LABEL_PASSWORD][:])
		username := string(sec.Data[util.LABEL_USERNAME][:])
		password := string(sec.Data[util.LABEL_PASSWORD][:])
		c := PgpoolPasswdFields{}
		c.Username = username
		c.Password = "md5" + util.GetMD5HashForAuthFile(password+username)
		creds = append(creds, c)

		//we will use the postgres user for pgpool to auth with
		if username == "postgres" {
			pgpoolUsername = username
			pgpoolPassword = password
		}
	}

	err = operator.PgpoolPasswdTemplate.Execute(&doc, creds)
	if err != nil {
		log.Error(err)
		return doc.Bytes(), pgpoolUsername, pgpoolPassword, err
	}
	log.Debug(doc.String())

	return doc.Bytes(), pgpoolUsername, pgpoolPassword, err
}
