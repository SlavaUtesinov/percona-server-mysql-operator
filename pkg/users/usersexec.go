package users

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

var sensitiveRegexp = regexp.MustCompile(":.*@")

type dbExecImpl struct {
	client *clientcmd.Client
	pod    *corev1.Pod
	user   apiv1alpha1.SystemUser
	pass   string
	host   string
}

func NewManagerExec(pod *corev1.Pod, user apiv1alpha1.SystemUser, pass, host string) (Manager, error) {
	c, err := clientcmd.NewClient()
	if err != nil {
		return nil, err
	}

	return &dbExecImpl{client: c, pod: pod, user: user, pass: pass, host: host}, nil
}

func (d *dbExecImpl) exec(stm string) error {

	cmd := []string{"mysql", "--database", "performance_schema", fmt.Sprintf("-p%s", escapePass(d.pass)), "-u", string(d.user), "-h", d.host, "-e", stm}

	var outb, errb bytes.Buffer
	err := d.client.Exec(context.TODO(), d.pod, "mysql", cmd, nil, &outb, &errb, false)
	if err != nil {
		sout := sensitiveRegexp.ReplaceAllString(outb.String(), ":*****@")
		serr := sensitiveRegexp.ReplaceAllString(errb.String(), ":*****@")
		return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, sout, serr)
	}

	if strings.Contains(errb.String(), "ERROR") {
		serr := sensitiveRegexp.ReplaceAllString(errb.String(), ":*****@")
		return fmt.Errorf("sql error: %s", serr)
	}

	return nil
}

// UpdateUserPasswords updates user passwords but retains the current password using Dual Password feature of MySQL 8
func (d *dbExecImpl) UpdateUserPasswords(users []mysql.User) error {
	err := d.exec("START TRANSACTION")
	if err != nil {
		return err
	}

	for _, user := range users {
		for _, host := range user.Hosts {
			q := fmt.Sprintf("ALTER USER '%s'@'%s' IDENTIFIED BY '%s' RETAIN CURRENT PASSWORD", user.Username, host, escapePass(user.Password))
			err = d.exec(q)
			if err != nil {
				err = errors.Wrap(err, "alter user")

				if errT := d.exec("ROLLBACK"); errT != nil {
					return errors.Wrap(errors.Wrap(errT, "rollback"), err.Error())
				}

				return err
			}
		}
	}

	err = d.exec("FLUSH PRIVILEGES")
	if err != nil {
		err = errors.Wrap(err, "flush privileges")

		if errT := d.exec("ROLLBACK"); errT != nil {
			return errors.Wrap(errors.Wrap(errT, "rollback"), err.Error())
		}

		return err
	}

	if err := d.exec("COMMIT"); err != nil {
		return errors.Wrap(err, "commit transaction")
	}

	return nil
}

// DiscardOldPasswords discards old passwords of givens users
func (d *dbExecImpl) DiscardOldPasswords(users []mysql.User) error {
	err := d.exec("START TRANSACTION")
	if err != nil {
		return err
	}

	for _, user := range users {
		for _, host := range user.Hosts {
			q := fmt.Sprintf("ALTER USER '%s'@'%s' DISCARD OLD PASSWORD", user.Username, host)
			err = d.exec(q)
			if err != nil {
				err = errors.Wrap(err, "discard old password")

				if errT := d.exec("ROLLBACK"); errT != nil {
					return errors.Wrap(errors.Wrap(errT, "rollback"), err.Error())
				}

				return err
			}
		}
	}

	err = d.exec("FLUSH PRIVILEGES")
	if err != nil {
		err = errors.Wrap(err, "flush privileges")

		if errT := d.exec("ROLLBACK"); errT != nil {
			return errors.Wrap(errors.Wrap(errT, "rollback"), err.Error())
		}

		return err
	}

	if err := d.exec("COMMIT"); err != nil {
		return errors.Wrap(err, "commit transaction")
	}

	return nil
}

func (d *dbExecImpl) Close() error {
	return nil
}

func escapePass(pass string) string {
	s := strings.ReplaceAll(pass, `'`, `\'`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `\`, `\\`)
	return s
}