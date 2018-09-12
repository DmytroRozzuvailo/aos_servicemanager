package launcher

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// Generates test image with python script
type pythonImage struct {
}

// Generates test image with iperf server
type iperfImage struct {
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var db *database.Database

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newTestLauncher(downloader downloadItf) (launcher *Launcher, err error) {
	launcher, err = New(&config.Config{WorkingDir: "tmp", DefaultServiceTTL: 30}, db)
	if err != nil {
		return launcher, err
	}

	launcher.downloader = downloader

	return launcher, err
}

func (downloader pythonImage) downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error) {
	imageDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		log.Error("Can't create image dir : ", err)
		return outputFile, err
	}
	defer os.RemoveAll(imageDir)

	// create dir
	if err := os.MkdirAll(path.Join(imageDir, "rootfs", "home"), 0755); err != nil {
		return outputFile, err
	}

	if err := generatePythonContent(imageDir); err != nil {
		return outputFile, err
	}

	if err := generateConfig(imageDir); err != nil {
		return outputFile, err
	}

	specFile := path.Join(imageDir, "config.json")

	spec, err := getServiceSpec(specFile)
	if err != nil {
		return outputFile, err
	}

	spec.Process.Args = []string{"python3", "/home/service.py", serviceInfo.ID, fmt.Sprintf("%d", serviceInfo.Version)}

	if spec.Annotations == nil {
		spec.Annotations = make(map[string]string)
	}
	spec.Annotations[aosProductPrefix+"vis.permissions"] = `{"*": "rw", "123": "rw"}`

	if err := writeServiceSpec(&spec, specFile); err != nil {
		return outputFile, err
	}

	imageFile, err := ioutil.TempFile("", "aos_")
	if err != nil {
		return outputFile, err
	}
	outputFile = imageFile.Name()
	imageFile.Close()

	if err = packImage(imageDir, outputFile); err != nil {
		return outputFile, err
	}

	return outputFile, nil
}

func (downloader iperfImage) downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error) {
	imageDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		log.Error("Can't create image dir : ", err)
		return outputFile, err
	}
	defer os.RemoveAll(imageDir)

	// create dir
	if err := os.MkdirAll(path.Join(imageDir, "rootfs", "home"), 0755); err != nil {
		return outputFile, err
	}

	if err := generateConfig(imageDir); err != nil {
		return outputFile, err
	}

	specFile := path.Join(imageDir, "config.json")

	spec, err := getServiceSpec(specFile)
	if err != nil {
		return outputFile, err
	}

	spec.Process.Args = []string{"iperf", "-s"}

	if spec.Annotations == nil {
		spec.Annotations = make(map[string]string)
	}
	spec.Annotations[aosProductPrefix+"network.upload"] = "4096"
	spec.Annotations[aosProductPrefix+"network.download"] = "8192"

	if err := writeServiceSpec(&spec, specFile); err != nil {
		return outputFile, err
	}

	imageFile, err := ioutil.TempFile("", "aos_")
	if err != nil {
		return outputFile, err
	}
	outputFile = imageFile.Name()
	imageFile.Close()

	if err = packImage(imageDir, outputFile); err != nil {
		return outputFile, err
	}

	return outputFile, nil
}

func (launcher *Launcher) removeAllServices() (err error) {
	services, err := launcher.db.GetServices()
	if err != nil {
		return err
	}

	statusChannel := make(chan error, len(services))

	for _, service := range services {
		go func(service database.ServiceEntry) {
			err := launcher.removeService(service.ID)
			if err != nil {
				log.Errorf("Can't remove service %s: %s", service.ID, err)
			}
			statusChannel <- err
		}(service)
	}

	// Wait all services are deleted
	for i := 0; i < len(services); i++ {
		<-statusChannel
	}

	err = launcher.systemd.Reload()
	if err != nil {
		return err
	}

	services, err = launcher.db.GetServices()
	if err != nil {
		return err
	}
	if len(services) != 0 {
		return errors.New("Can't remove all services")
	}

	if err = launcher.cleanUsersDB(); err != nil {
		return err
	}

	return err
}

func setup() (err error) {
	if err := os.MkdirAll("tmp", 0755); err != nil {
		return err
	}

	db, err = database.New("tmp/servicemanager.db")
	if err != nil {
		return err
	}

	return nil
}

func cleanup() (err error) {
	launcher, err := newTestLauncher(new(pythonImage))
	if err != nil {
		return err
	}
	defer launcher.Close()

	if err := launcher.removeAllServices(); err != nil {
		return err
	}

	db.Close()

	if err := os.RemoveAll("tmp"); err != nil {
		return err
	}

	return nil
}

func generatePythonContent(imagePath string) (err error) {
	serviceContent := `#!/usr/bin/python

import time
import socket
import sys

i = 0
serviceName = sys.argv[1]
serviceVersion = sys.argv[2]

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM) 
message = serviceName + ", version: " + serviceVersion
sock.sendto(str.encode(message), ("172.19.0.1", 10001))
sock.close()

print(">>>> Start", serviceName, "version", serviceVersion)
while True:
	print(">>>> aos", serviceName, "version", serviceVersion, "count", i)
	i = i + 1
	time.sleep(5)`

	if err := ioutil.WriteFile(path.Join(imagePath, "rootfs", "home", "service.py"), []byte(serviceContent), 0644); err != nil {
		return err
	}

	return nil
}

func generateConfig(imagePath string) (err error) {
	// remove json
	if err := os.Remove(path.Join(imagePath, "config.json")); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}

	// generate config spec
	out, err := exec.Command("runc", "spec", "-b", imagePath).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	return nil
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {

	if err := setup(); err != nil {
		log.Fatalf("Error creating service images: %s", err)
	}

	ret := m.Run()

	if err := cleanup(); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestInstallRemove(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage))
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	numInstallServices := 10
	numRemoveServices := 5

	// install services
	for i := 0; i < numInstallServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("service%d", i)})
	}
	// remove services
	for i := 0; i < numRemoveServices; i++ {
		launcher.RemoveService(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numInstallServices+numRemoveServices; i++ {
		if status := <-launcher.StatusChannel; status.Err != nil {
			if status.Action == ActionInstall {
				t.Errorf("Can't install service %s: %s", status.ID, status.Err)
			} else {
				t.Errorf("Can't remove service %s: %s", status.ID, status.Err)
			}
		}
	}

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != numInstallServices-numRemoveServices {
		t.Errorf("Wrong service quantity")
	}
	for _, service := range services {
		if service.Status != "OK" {
			t.Errorf("Service %s error status: %s", service.ID, service.Status)
		}
	}

	time.Sleep(time.Second * 2)

	// remove remaining services
	for i := numRemoveServices; i < numInstallServices; i++ {
		launcher.RemoveService(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numInstallServices-numRemoveServices; i++ {
		if status := <-launcher.StatusChannel; status.Err != nil {
			t.Errorf("Can't remove service %s: %s", status.ID, status.Err)
		}
	}

	services, err = launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != 0 {
		t.Errorf("Wrong service quantity")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestAutoStart(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage))
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	numServices := 5

	// install services
	for i := 0; i < numServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("service%d", i)})
	}

	for i := 0; i < numServices; i++ {
		if status := <-launcher.StatusChannel; status.Err != nil {
			t.Errorf("Can't install service %s: %s", status.ID, status.Err)
		}
	}

	launcher.Close()

	time.Sleep(time.Second * 2)

	launcher, err = newTestLauncher(new(pythonImage))
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	time.Sleep(time.Second * 2)

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != numServices {
		t.Errorf("Wrong service quantity")
	}
	for _, service := range services {
		if service.Status != "OK" {
			t.Errorf("Service %s error status: %s", service.ID, service.Status)
		}
	}

	// remove services
	for i := 0; i < numServices; i++ {
		launcher.RemoveService(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numServices; i++ {
		if status := <-launcher.StatusChannel; status.Err != nil {
			t.Errorf("Can't remove service %s: %s", status.ID, status.Err)
		}
	}

	services, err = launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != 0 {
		t.Errorf("Wrong service quantity")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestErrors(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage))
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	// test version mistmatch

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 5})
	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 4})
	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 6})

	for i := 0; i < 3; i++ {
		status := <-launcher.StatusChannel
		switch {
		case status.Version == 5 && status.Err != nil:
			t.Errorf("Can't install service %s version %d: %s", status.ID, status.Version, status.Err)
		case status.Version == 4 && status.Err == nil:
			t.Errorf("Service %s version %d should not be installed", status.ID, status.Version)
		case status.Version == 6 && status.Err != nil:
			t.Errorf("Can't install service %s version %d: %s", status.ID, status.Version, status.Err)
		}
	}

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != 1 {
		t.Errorf("Wrong service quantity: %d", len(services))
	} else if services[0].Version != 6 {
		t.Errorf("Wrong service version")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestUpdate(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage))
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	serverAddr, err := net.ResolveUDPAddr("udp", ":10001")
	if err != nil {
		t.Fatalf("Can't create resolve UDP address: %s", err)
	}

	serverConn, err := net.ListenUDP("udp", serverAddr)
	if err != nil {
		t.Fatalf("Can't listen UDP: %s", err)
	}
	defer serverConn.Close()

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 0})

	if status := <-launcher.StatusChannel; status.Err != nil {
		t.Fatalf("Can't install %s service: %s", status.ID, status.Err)
	}

	if err := serverConn.SetReadDeadline(time.Now().Add(time.Second * 30)); err != nil {
		t.Fatalf("Can't set read deadline: %s", err)
	}

	buf := make([]byte, 1024)

	n, _, err := serverConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("Can't read from UDP: %s", err)
	} else {
		message := string(buf[:n])

		if message != "service0, version: 0" {
			t.Fatalf("Wrong service content: %s", message)
		}
	}

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 1})

	if status := <-launcher.StatusChannel; status.Err != nil {
		t.Fatalf("Can't install %s service: %s", status.ID, status.Err)
	}

	n, _, err = serverConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("Can't read from UDP: %s", err)
	} else {
		message := string(buf[:n])

		if message != "service0, version: 1" {
			t.Fatalf("Wrong service content: %s", message)
		}
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestNetworkSpeed(t *testing.T) {
	launcher, err := newTestLauncher(new(iperfImage))
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	numServices := 2

	for i := 0; i < numServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("service%d", i)})
	}

	for i := 0; i < numServices; i++ {
		if status := <-launcher.StatusChannel; status.Err != nil {
			t.Errorf("Can't install service %s: %s", status.ID, status.Err)
		}
	}

	for i := 0; i < numServices; i++ {
		serviceID := fmt.Sprintf("service%d", i)

		addr, err := launcher.GetServiceIPAddress(serviceID)
		if err != nil {
			t.Errorf("Can't get ip address: %s", err)
			continue
		}
		output, err := exec.Command("iperf", "-c"+addr, "-d", "-r", "-t2", "-yc").Output()
		if err != nil {
			t.Errorf("Iperf failed: %s", err)
			continue
		}

		ulSpeed := -1
		dlSpeed := -1

		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			result := strings.Split(line, ",")
			if len(result) >= 9 {
				if result[4] == "5001" {
					value, err := strconv.ParseInt(result[8], 10, 64)
					if err != nil {
						t.Errorf("Can't parse ul speed: %s", err)
						continue
					}
					ulSpeed = int(value) / 1000
				} else {
					value, err := strconv.ParseUint(result[8], 10, 64)
					if err != nil {
						t.Errorf("Can't parse ul speed: %s", err)
						continue
					}
					dlSpeed = int(value) / 1000
				}
			}
		}

		if ulSpeed == -1 || dlSpeed == -1 {
			t.Error("Can't determine ul/dl speed")
		}

		if ulSpeed > 4096*1.5 || dlSpeed > 8192*1.5 {
			t.Errorf("Speed limit exceeds: dl %d, ul %d", dlSpeed, ulSpeed)
		}
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestVisPermissions(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage))
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 0})

	if status := <-launcher.StatusChannel; status.Err != nil {
		t.Fatalf("Can't install %s service: %s", status.ID, status.Err)
	}

	service, err := db.GetService("service0")
	if err != nil {
		t.Fatalf("Can't get service: %s", err)
	}

	if service.Permissions != `{"*": "rw", "123": "rw"}` {
		t.Fatalf("Permissions mismatch")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestUsersServices(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage))
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	numUsers := 3
	numServices := 3

	for i := 0; i < numUsers; i++ {
		users := []string{fmt.Sprintf("user%d", i)}

		if err = launcher.SetUsers(users); err != nil {
			t.Fatalf("Can't set users: %s", err)
		}

		services, err := launcher.db.GetUsersServices(users)
		if err != nil {
			t.Fatalf("Can't get users services: %s", err)
		}
		if len(services) != 0 {
			t.Fatalf("Wrong service quantity")
		}

		// install services
		for j := 0; j < numServices; j++ {
			launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("user%d_service%d", i, j)})
		}
		for i := 0; i < numServices; i++ {
			if status := <-launcher.StatusChannel; status.Err != nil {
				t.Errorf("Can't install service %s: %s", status.ID, status.Err)
			}
		}

		time.Sleep(time.Second * 2)

		services, err = launcher.db.GetServices()
		if err != nil {
			t.Fatalf("Can't get services: %s", err)
		}

		count := 0
		for _, service := range services {
			if service.State == stateRunning {
				exist, err := launcher.db.IsUsersService(users, service.ID)
				if err != nil {
					t.Errorf("Can't check users service: %s", err)
				}
				if !exist {
					t.Errorf("Service doesn't belong to users: %s", service.ID)
				}
				count++
			}
		}

		if count != numServices {
			t.Fatalf("Wrong running services count")
		}
	}

	for i := 0; i < numUsers; i++ {
		users := []string{fmt.Sprintf("user%d", i)}

		if err = launcher.SetUsers(users); err != nil {
			t.Fatalf("Can't set users: %s", err)
		}

		time.Sleep(time.Second * 2)

		services, err := launcher.db.GetServices()
		if err != nil {
			t.Fatalf("Can't get services: %s", err)
		}

		count := 0
		for _, service := range services {
			if service.State == stateRunning {
				exist, err := launcher.db.IsUsersService(users, service.ID)
				if err != nil {
					t.Errorf("Can't check users service: %s", err)
				}
				if !exist {
					t.Errorf("Service doesn't belong to users: %s", service.ID)
				}
				count++
			}
		}

		if count != numServices {
			t.Fatalf("Wrong running services count")
		}
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestServiceTTL(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage))
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	numServices := 3

	if err = launcher.SetUsers([]string{"user0"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	// install services
	for i := 0; i < numServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("service%d", i)})
	}
	for i := 0; i < numServices; i++ {
		if status := <-launcher.StatusChannel; status.Err != nil {
			t.Errorf("Can't install service %s: %s", status.ID, status.Err)
		}
	}

	services, err := launcher.db.GetServices()
	if err != nil {
		t.Fatalf("Can't get services: %s", err)
	}

	for _, service := range services {
		if err = launcher.db.SetServiceStartTime(service.ID, service.StartAt.Add(-time.Hour*24*30)); err != nil {
			t.Errorf("Can't set service start time: %s", err)
		}
	}

	if err = launcher.SetUsers([]string{"user1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	services, err = launcher.db.GetServices()
	if err != nil {
		t.Fatalf("Can't get services: %s", err)
	}

	if len(services) != 0 {
		t.Fatal("Wrong service quantity")
	}

	usersList, err := launcher.db.GetUsersList()
	if err != nil {
		t.Fatalf("Can't get users list: %s", err)
	}

	if len(usersList) != 0 {
		t.Fatal("Wrong users quantity", usersList)
	}
}