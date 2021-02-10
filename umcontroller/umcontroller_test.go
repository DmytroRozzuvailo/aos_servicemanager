// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2020 Renesas Inc.
// Copyright 2020 EPAM Systems Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package umcontroller_test

import (
	"context"
	"io"
	"io/ioutil"
	"os"
	"path"
	"reflect"
	"strconv"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	pb "gitpct.epam.com/epmd-aepr/aos_common/api/updatemanager"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/umcontroller"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	serverURL = "localhost:8091"
)

type testStatusSender struct {
	Components []amqp.ComponentInfo
}

type testStorage struct {
	updateInfo []umcontroller.SystemComponent
}
type testDownloader struct {
}

type testNotifyDownloader struct {
	downloadSyncCh chan bool
	componetCount  int
	curretCount    int
}

type testUmConnection struct {
	stream         pb.UpdateController_RegisterUMClient
	notifyTestChan chan bool
	continueChan   chan bool
	step           string
	test           *testing.T
	umId           string
	components     []*pb.SystemComponent
	conn           *grpc.ClientConn
}

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
 * Main
 ******************************************************************************/

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestConnection(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	umCtrlConfig := config.UmController{
		ServerURL: "localhost:8091",
		UmClients: []config.UmClientConfig{
			config.UmClientConfig{UmID: "umID1", Priority: 10},
			config.UmClientConfig{UmID: "umID2", Priority: 0}},
		UpdateDir: tmpDir,
	}
	smConfig := config.Config{UmController: umCtrlConfig}

	umCtrl, err := umcontroller.New(&smConfig, &testStatusSender{}, &testStorage{}, &testDownloader{}, true)
	if err != nil {
		t.Fatalf("Can't create: UM controller %s", err)
	}

	components := []*pb.SystemComponent{&pb.SystemComponent{Id: "component1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "component2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	streamUM1, connUM1, err := createClientConnection("umID1", pb.UmState_IDLE, components)
	if err != nil {
		t.Errorf("Error connect %s", err)
	}

	components2 := []*pb.SystemComponent{&pb.SystemComponent{Id: "component3", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "component4", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	streamUM2, connUM2, err := createClientConnection("umID2", pb.UmState_IDLE, components2)
	if err != nil {
		t.Errorf("Error connect %s", err)
	}

	streamUM1_copy, connUM1_copy, err := createClientConnection("umID1", pb.UmState_IDLE, components)
	if err != nil {
		t.Errorf("Error connect %s", err)
	}

	umCtrl.Close()

	newComponents, err := umCtrl.GetSystemComponents()
	if err != nil {
		t.Errorf("Can't get system components %s", err)
	}

	if len(newComponents) != 4 {
		t.Errorf("Incorrect count of components %d", len(newComponents))
	}

	streamUM1.CloseSend()
	connUM1.Close()

	streamUM2.CloseSend()
	connUM2.Close()

	streamUM1_copy.CloseSend()
	connUM1_copy.Close()

	time.Sleep(1 * time.Second)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func createClientConnection(clientID string, state pb.UmState,
	components []*pb.SystemComponent) (stream pb.UpdateController_RegisterUMClient, conn *grpc.ClientConn, err error) {
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithInsecure())
	opts = append(opts, grpc.WithBlock())

	conn, err = grpc.Dial(serverURL, opts...)
	if err != nil {
		return stream, nil, err
	}

	client := pb.NewUpdateControllerClient(conn)
	stream, err = client.RegisterUM(context.Background())
	if err != nil {
		log.Errorf("Fail call RegisterUM %s", err)
		return stream, nil, err
	}

	umMsg := &pb.UpdateStatus{UmId: clientID, UmState: state, Components: components}

	if err = stream.Send(umMsg); err != nil {
		log.Errorf("Fail send update status message %s", err)
	}
	time.Sleep(1 * time.Second)

	return stream, conn, nil
}

func TestFullUpdate(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	umCtrlConfig := config.UmController{
		ServerURL: "localhost:8091",
		UmClients: []config.UmClientConfig{
			config.UmClientConfig{UmID: "testUM1", Priority: 1},
			config.UmClientConfig{UmID: "testUM2", Priority: 10}},
		UpdateDir: tmpDir,
	}

	smConfig := config.Config{UmController: umCtrlConfig}

	var updateSender testStatusSender
	var updateStorage testStorage

	updateDownloader := testNotifyDownloader{downloadSyncCh: make(chan bool, 1), componetCount: 3}

	umCtrl, err := umcontroller.New(&smConfig, &updateSender, &updateStorage, &updateDownloader, true)
	if err != nil {
		t.Errorf("Can't create: UM controller %s", err)
	}

	um1Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um1C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um1C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um1 := newTestUM("testUM1", pb.UmState_IDLE, "init", um1Components, t)
	go um1.processMessages()

	um2Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um2C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um2C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um2 := newTestUM("testUM2", pb.UmState_IDLE, "init", um2Components, t)
	go um2.processMessages()

	desiredStatus := []amqp.ComponentInfoFromCloud{amqp.ComponentInfoFromCloud{ID: "um1C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "1"}},
		amqp.ComponentInfoFromCloud{ID: "um1C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um2C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um2C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
	}

	finishChanel := make(chan bool)

	go func(finChan chan bool) {
		umCtrl.ProcessDesiredComponents(desiredStatus, []amqp.CertificateChain{}, []amqp.Certificate{})
		finChan <- true
	}(finishChanel)

	<-updateDownloader.downloadSyncCh

	um1Components = append(um1Components, &pb.SystemComponent{Id: "um1C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um1.setComponents(um1Components)

	um1.step = "prepare"
	um1.continueChan <- true
	<-um1.notifyTestChan // receive prepare
	um1.sendState(pb.UmState_PREPARED)

	um2Components = append(um2Components, &pb.SystemComponent{Id: "um2C1", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um2Components = append(um2Components, &pb.SystemComponent{Id: "um2C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um2.setComponents(um2Components)

	um2.step = "prepare"
	um2.continueChan <- true
	<-um2.notifyTestChan
	um2.sendState(pb.UmState_PREPARED)

	um1.step = "update"
	um1.continueChan <- true
	<-um1.notifyTestChan //um1 updated
	um1.sendState(pb.UmState_UPDATED)

	um2.step = "update"
	um2.continueChan <- true
	<-um2.notifyTestChan //um2 updated
	um2.sendState(pb.UmState_UPDATED)

	um1Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um1C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um1C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLED}}
	um1.setComponents(um1Components)

	um1.step = "apply"
	um1.continueChan <- true
	<-um1.notifyTestChan //um1 apply
	um1.sendState(pb.UmState_IDLE)

	um2Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um2C1", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um2C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLED}}
	um2.setComponents(um2Components)

	um2.step = "apply"
	um2.continueChan <- true
	<-um2.notifyTestChan //um1 apply
	um2.sendState(pb.UmState_IDLE)

	time.Sleep(1 * time.Second)
	um1.step = "finish"
	um2.step = "finish"

	<-finishChanel

	um1.closeConnection()
	um2.closeConnection()

	<-um1.notifyTestChan
	<-um2.notifyTestChan

	umCtrl.Close()

	etalonComponents := []amqp.ComponentInfo{amqp.ComponentInfo{ID: "um1C1", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um1C2", VendorVersion: "2", Status: "installed"},
		amqp.ComponentInfo{ID: "um2C1", VendorVersion: "2", Status: "installed"},
		amqp.ComponentInfo{ID: "um2C2", VendorVersion: "2", Status: "installed"}}

	if !reflect.DeepEqual(etalonComponents, updateSender.Components) {
		log.Debug(updateSender.Components)
		t.Error("incorrect result component list")
	}

	time.Sleep(time.Second)
}

func TestFullUpdateWithDisconnect(t *testing.T) {
	// TODO: fix the test on CI
	if os.Getenv("CI") != "" {
		t.Skip("Skipping testing in CI environment")
	}

	tmpDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	umCtrlConfig := config.UmController{
		ServerURL: "localhost:8091",
		UmClients: []config.UmClientConfig{
			config.UmClientConfig{UmID: "testUM3", Priority: 1},
			config.UmClientConfig{UmID: "testUM4", Priority: 10}},
		UpdateDir: tmpDir,
	}

	smConfig := config.Config{UmController: umCtrlConfig}

	var updateSender testStatusSender
	var updateStorage testStorage

	updateDownloader := testNotifyDownloader{downloadSyncCh: make(chan bool, 1), componetCount: 3}

	umCtrl, err := umcontroller.New(&smConfig, &updateSender, &updateStorage, &updateDownloader, true)
	if err != nil {
		t.Errorf("Can't create: UM controller %s", err)
	}

	um3Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um3C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um3C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um3 := newTestUM("testUM3", pb.UmState_IDLE, "init", um3Components, t)
	go um3.processMessages()

	um4Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um4C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um4C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um4 := newTestUM("testUM4", pb.UmState_IDLE, "init", um4Components, t)
	go um4.processMessages()

	desiredStatus := []amqp.ComponentInfoFromCloud{amqp.ComponentInfoFromCloud{ID: "um3C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "1"}},
		amqp.ComponentInfoFromCloud{ID: "um3C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um4C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um4C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
	}

	finishChanel := make(chan bool)

	go func() {
		umCtrl.ProcessDesiredComponents(desiredStatus, []amqp.CertificateChain{}, []amqp.Certificate{})
		close(finishChanel)
	}()

	<-updateDownloader.downloadSyncCh

	// prepare UM3
	um3Components = append(um3Components, &pb.SystemComponent{Id: "um3C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um3.setComponents(um3Components)

	um3.step = "prepare"
	um3.continueChan <- true
	<-um3.notifyTestChan // receive prepare
	um3.sendState(pb.UmState_PREPARED)

	// prepare UM4
	um4Components = append(um4Components, &pb.SystemComponent{Id: "um4C1", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um4Components = append(um4Components, &pb.SystemComponent{Id: "um4C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um4.setComponents(um4Components)

	um4.step = "prepare"
	um4.continueChan <- true
	<-um4.notifyTestChan
	um4.sendState(pb.UmState_PREPARED)

	um3.step = "update"
	um3.continueChan <- true
	<-um3.notifyTestChan

	// um3  reboot
	um3.step = "reboot"
	um3.closeConnection()
	<-um3.notifyTestChan

	um3 = newTestUM("testUM3", pb.UmState_UPDATED, "apply", um3Components, t)
	go um3.processMessages()

	um4.step = "update"
	um4.continueChan <- true
	<-um4.notifyTestChan
	// full reboot
	um4.step = "reboot"
	um4.closeConnection()

	<-um4.notifyTestChan

	um4 = newTestUM("testUM4", pb.UmState_UPDATED, "apply", um4Components, t)
	go um4.processMessages()

	um3.step = "apply"
	um3.continueChan <- true
	<-um3.notifyTestChan

	// um3  reboot
	um3.step = "reboot"
	um3.closeConnection()
	<-um3.notifyTestChan

	um3Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um3C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um3C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLED}}
	um3 = newTestUM("testUM3", pb.UmState_IDLE, "init", um3Components, t)
	go um3.processMessages()

	// um4  reboot
	um4.step = "reboot"
	um4.closeConnection()
	<-um4.notifyTestChan

	um4Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um4C1", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um4C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLED}}
	um4 = newTestUM("testUM4", pb.UmState_IDLE, "init", um4Components, t)
	go um4.processMessages()

	um3.step = "finish"
	um4.step = "finish"

	<-finishChanel

	um3.closeConnection()
	um4.closeConnection()

	<-um3.notifyTestChan
	<-um4.notifyTestChan

	umCtrl.Close()

	etalonComponents := []amqp.ComponentInfo{amqp.ComponentInfo{ID: "um3C1", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um3C2", VendorVersion: "2", Status: "installed"},
		amqp.ComponentInfo{ID: "um4C1", VendorVersion: "2", Status: "installed"},
		amqp.ComponentInfo{ID: "um4C2", VendorVersion: "2", Status: "installed"}}

	if !reflect.DeepEqual(etalonComponents, updateSender.Components) {
		log.Debug(updateSender.Components)
		t.Error("incorrect result component list")
	}

	time.Sleep(time.Second)
}

func TestFullUpdateWithReboot(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	umCtrlConfig := config.UmController{
		ServerURL: "localhost:8091",
		UmClients: []config.UmClientConfig{
			config.UmClientConfig{UmID: "testUM5", Priority: 1},
			config.UmClientConfig{UmID: "testUM6", Priority: 10}},
		UpdateDir: tmpDir,
	}

	smConfig := config.Config{UmController: umCtrlConfig}

	var updateSender testStatusSender
	var updateStorage testStorage

	updateDownloader := testNotifyDownloader{downloadSyncCh: make(chan bool, 1), componetCount: 3}

	umCtrl, err := umcontroller.New(&smConfig, &updateSender, &updateStorage, &updateDownloader, true)
	if err != nil {
		t.Errorf("Can't create: UM controller %s", err)
	}

	um5Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um5C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um5C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um5 := newTestUM("testUM5", pb.UmState_IDLE, "init", um5Components, t)
	go um5.processMessages()

	um6Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um6C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um6C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um6 := newTestUM("testUM6", pb.UmState_IDLE, "init", um6Components, t)
	go um6.processMessages()

	desiredStatus := []amqp.ComponentInfoFromCloud{amqp.ComponentInfoFromCloud{ID: "um5C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "1"}},
		amqp.ComponentInfoFromCloud{ID: "um5C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um6C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um6C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
	}

	finishChanel := make(chan bool)

	go func() {
		umCtrl.ProcessDesiredComponents(desiredStatus, []amqp.CertificateChain{}, []amqp.Certificate{})
		close(finishChanel)
	}()

	<-updateDownloader.downloadSyncCh

	// prepare UM5
	um5Components = append(um5Components, &pb.SystemComponent{Id: "um5C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um5.setComponents(um5Components)

	um5.step = "prepare"
	um5.continueChan <- true
	<-um5.notifyTestChan // receive prepare
	um5.sendState(pb.UmState_PREPARED)

	// prepare UM6
	um6Components = append(um6Components, &pb.SystemComponent{Id: "um6C1", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um6Components = append(um6Components, &pb.SystemComponent{Id: "um6C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um6.setComponents(um6Components)

	um6.step = "prepare"
	um6.continueChan <- true
	<-um6.notifyTestChan
	um6.sendState(pb.UmState_PREPARED)

	um5.step = "update"
	um5.continueChan <- true
	<-um5.notifyTestChan

	// full reboot
	um5.step = "reboot"
	um6.step = "reboot"

	um5.closeConnection()
	um6.closeConnection()
	umCtrl.Close()

	<-um5.notifyTestChan
	<-um6.notifyTestChan
	<-finishChanel

	umCtrl, err = umcontroller.New(&smConfig, &updateSender, &updateStorage, &updateDownloader, true)
	if err != nil {
		t.Errorf("Can't create: UM controller %s", err)
	}

	um5 = newTestUM("testUM5", pb.UmState_UPDATED, "apply", um5Components, t)
	go um5.processMessages()

	um6 = newTestUM("testUM6", pb.UmState_PREPARED, "update", um6Components, t)
	go um6.processMessages()

	um6.continueChan <- true
	<-um6.notifyTestChan

	um6.step = "reboot"
	um6.closeConnection()

	um6 = newTestUM("testUM6", pb.UmState_UPDATED, "apply", um6Components, t)
	go um6.processMessages()

	// um5 apply and full reboot
	um5.continueChan <- true
	<-um5.notifyTestChan

	// full reboot
	um5.step = "reboot"
	um6.step = "reboot"

	um5.closeConnection()
	um6.closeConnection()
	umCtrl.Close()

	<-um5.notifyTestChan
	<-um6.notifyTestChan

	umCtrl, err = umcontroller.New(&smConfig, &updateSender, &updateStorage, &updateDownloader, true)
	if err != nil {
		t.Errorf("Can't create: UM controller %s", err)
	}

	um5Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um5C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um5C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLED}}
	um5 = newTestUM("testUM5", pb.UmState_IDLE, "init", um5Components, t)
	go um5.processMessages()

	um6 = newTestUM("testUM6", pb.UmState_UPDATED, "apply", um6Components, t)
	go um6.processMessages()

	um6.step = "reboot"
	um6.closeConnection()
	<-um6.notifyTestChan

	um6Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um6C1", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um6C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLED}}
	um6 = newTestUM("testUM6", pb.UmState_IDLE, "init", um6Components, t)
	go um6.processMessages()

	um5.step = "finish"
	um6.step = "finish"

	um5.closeConnection()
	um6.closeConnection()

	<-um5.notifyTestChan
	<-um6.notifyTestChan

	umCtrl.Close()

	etalonComponents := []amqp.ComponentInfo{amqp.ComponentInfo{ID: "um5C1", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um5C2", VendorVersion: "2", Status: "installed"},
		amqp.ComponentInfo{ID: "um6C1", VendorVersion: "2", Status: "installed"},
		amqp.ComponentInfo{ID: "um6C2", VendorVersion: "2", Status: "installed"}}

	if !reflect.DeepEqual(etalonComponents, updateSender.Components) {
		log.Debug(updateSender.Components)
		t.Error("incorrect result component list")
	}

	time.Sleep(time.Second)
}

func TestRevertOnPrepare(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	umCtrlConfig := config.UmController{
		ServerURL: "localhost:8091",
		UmClients: []config.UmClientConfig{
			config.UmClientConfig{UmID: "testUM7", Priority: 1},
			config.UmClientConfig{UmID: "testUM8", Priority: 10}},
		UpdateDir: tmpDir,
	}

	smConfig := config.Config{UmController: umCtrlConfig}

	var updateSender testStatusSender
	var updateStorage testStorage

	updateDownloader := testNotifyDownloader{downloadSyncCh: make(chan bool, 1), componetCount: 3}

	umCtrl, err := umcontroller.New(&smConfig, &updateSender, &updateStorage, &updateDownloader, true)
	if err != nil {
		t.Errorf("Can't create: UM controller %s", err)
	}

	um7Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um7C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um7C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um7 := newTestUM("testUM7", pb.UmState_IDLE, "init", um7Components, t)
	go um7.processMessages()

	um8Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um8C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um8C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um8 := newTestUM("testUM8", pb.UmState_IDLE, "init", um8Components, t)
	go um8.processMessages()

	desiredStatus := []amqp.ComponentInfoFromCloud{amqp.ComponentInfoFromCloud{ID: "um7C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "1"}},
		amqp.ComponentInfoFromCloud{ID: "um7C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um8C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um8C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
	}

	finishChanel := make(chan bool)

	go func() {
		umCtrl.ProcessDesiredComponents(desiredStatus, []amqp.CertificateChain{}, []amqp.Certificate{})
		close(finishChanel)
	}()

	<-updateDownloader.downloadSyncCh

	um7Components = append(um7Components, &pb.SystemComponent{Id: "um7C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um7.setComponents(um7Components)

	um7.step = "prepare"
	um7.continueChan <- true
	<-um7.notifyTestChan // receive prepare
	um7.sendState(pb.UmState_PREPARED)

	um8Components = append(um8Components, &pb.SystemComponent{Id: "um8C1", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um8Components = append(um8Components, &pb.SystemComponent{Id: "um8C2", VendorVersion: "2", Status: pb.ComponentStatus_ERROR})
	um8.setComponents(um8Components)

	um8.step = "prepare"
	um8.continueChan <- true
	<-um8.notifyTestChan
	um8.sendState(pb.UmState_FAILED)

	um7Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um7C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um7C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um7.step = "revert"
	um7.continueChan <- true
	<-um7.notifyTestChan //um7 revert received
	um7.sendState(pb.UmState_IDLE)

	um8Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um8C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um8C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um8C2", VendorVersion: "2", Status: pb.ComponentStatus_ERROR}}

	um8.step = "revert"
	um8.continueChan <- true
	<-um8.notifyTestChan //um8 revert received
	um8.sendState(pb.UmState_IDLE)

	um7.step = "finish"
	um8.step = "finish"

	<-finishChanel

	um7.closeConnection()
	um8.closeConnection()

	<-um7.notifyTestChan
	<-um8.notifyTestChan

	umCtrl.Close()

	etalonComponents := []amqp.ComponentInfo{amqp.ComponentInfo{ID: "um7C1", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um7C2", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um8C1", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um8C2", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um8C2", VendorVersion: "2", Status: "error"}}

	if !reflect.DeepEqual(etalonComponents, updateSender.Components) {
		log.Debug(updateSender.Components)
		t.Error("incorrect result component list")
		log.Debug(etalonComponents)
	}

	time.Sleep(time.Second)
}

func TestRevertOnUpdate(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	umCtrlConfig := config.UmController{
		ServerURL: "localhost:8091",
		UmClients: []config.UmClientConfig{
			config.UmClientConfig{UmID: "testUM9", Priority: 1},
			config.UmClientConfig{UmID: "testUM10", Priority: 10}},
		UpdateDir: tmpDir,
	}

	smConfig := config.Config{UmController: umCtrlConfig}

	var updateSender testStatusSender
	var updateStorage testStorage

	updateDownloader := testNotifyDownloader{downloadSyncCh: make(chan bool, 1), componetCount: 3}

	umCtrl, err := umcontroller.New(&smConfig, &updateSender, &updateStorage, &updateDownloader, true)
	if err != nil {
		t.Errorf("Can't create: UM controller %s", err)
	}

	um9Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um9C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um9C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um9 := newTestUM("testUM9", pb.UmState_IDLE, "init", um9Components, t)
	go um9.processMessages()

	um10Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um10C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um10C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um10 := newTestUM("testUM10", pb.UmState_IDLE, "init", um10Components, t)
	go um10.processMessages()

	desiredStatus := []amqp.ComponentInfoFromCloud{amqp.ComponentInfoFromCloud{ID: "um9C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "1"}},
		amqp.ComponentInfoFromCloud{ID: "um9C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um10C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um10C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
	}

	finishChanel := make(chan bool)

	go func() {
		umCtrl.ProcessDesiredComponents(desiredStatus, []amqp.CertificateChain{}, []amqp.Certificate{})
		close(finishChanel)
	}()

	<-updateDownloader.downloadSyncCh

	um9Components = append(um9Components, &pb.SystemComponent{Id: "um9C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um9.setComponents(um9Components)

	um9.step = "prepare"
	um9.continueChan <- true
	<-um9.notifyTestChan // receive prepare
	um9.sendState(pb.UmState_PREPARED)

	um10Components = append(um10Components, &pb.SystemComponent{Id: "um10C1", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um10Components = append(um10Components, &pb.SystemComponent{Id: "um10C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um10.setComponents(um10Components)

	um10.step = "prepare"
	um10.continueChan <- true
	<-um10.notifyTestChan
	um10.sendState(pb.UmState_PREPARED)

	um9.step = "update"
	um9.continueChan <- true
	<-um9.notifyTestChan //um9 updated
	um9.sendState(pb.UmState_UPDATED)

	um10Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um10C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um10C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um10C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING},
		&pb.SystemComponent{Id: "um10C2", VendorVersion: "2", Status: pb.ComponentStatus_ERROR}}
	um10.setComponents(um10Components)

	um10.step = "update"
	um10.continueChan <- true
	<-um10.notifyTestChan //um10 updated
	um10.sendState(pb.UmState_FAILED)

	um9Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um9C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um9C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}
	um9.setComponents(um9Components)

	um9.step = "revert"
	um9.continueChan <- true
	<-um9.notifyTestChan //um9 revert received
	um9.sendState(pb.UmState_IDLE)

	um10Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um10C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um10C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um10C2", VendorVersion: "2", Status: pb.ComponentStatus_ERROR}}

	um10.step = "revert"
	um10.continueChan <- true
	<-um10.notifyTestChan //um10 revert received
	um10.sendState(pb.UmState_IDLE)

	um9.step = "finish"
	um10.step = "finish"

	<-finishChanel

	um9.closeConnection()
	um10.closeConnection()

	<-um9.notifyTestChan
	<-um10.notifyTestChan

	umCtrl.Close()

	etalonComponents := []amqp.ComponentInfo{amqp.ComponentInfo{ID: "um9C1", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um9C2", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um10C1", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um10C2", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um10C2", VendorVersion: "2", Status: "error"}}

	if !reflect.DeepEqual(etalonComponents, updateSender.Components) {
		log.Debug(updateSender.Components)
		t.Error("incorrect result component list")
		log.Debug(etalonComponents)
	}

	time.Sleep(time.Second)
}

func TestRevertOnUpdateWithDisconnect(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	umCtrlConfig := config.UmController{
		ServerURL: "localhost:8091",
		UmClients: []config.UmClientConfig{
			config.UmClientConfig{UmID: "testUM11", Priority: 1},
			config.UmClientConfig{UmID: "testUM12", Priority: 10}},
		UpdateDir: tmpDir,
	}

	smConfig := config.Config{UmController: umCtrlConfig}

	var updateSender testStatusSender
	var updateStorage testStorage

	updateDownloader := testNotifyDownloader{downloadSyncCh: make(chan bool, 1), componetCount: 3}

	umCtrl, err := umcontroller.New(&smConfig, &updateSender, &updateStorage, &updateDownloader, true)
	if err != nil {
		t.Errorf("Can't create: UM controller %s", err)
	}

	um11Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um11C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um11C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um11 := newTestUM("testUM11", pb.UmState_IDLE, "init", um11Components, t)
	go um11.processMessages()

	um12Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um12C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um12C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um12 := newTestUM("testUM12", pb.UmState_IDLE, "init", um12Components, t)
	go um12.processMessages()

	desiredStatus := []amqp.ComponentInfoFromCloud{amqp.ComponentInfoFromCloud{ID: "um11C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "1"}},
		amqp.ComponentInfoFromCloud{ID: "um11C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um12C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um12C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
	}

	finishChanel := make(chan bool)

	go func() {
		umCtrl.ProcessDesiredComponents(desiredStatus, []amqp.CertificateChain{}, []amqp.Certificate{})
		close(finishChanel)
	}()

	<-updateDownloader.downloadSyncCh

	um11Components = append(um11Components, &pb.SystemComponent{Id: "um11C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um11.setComponents(um11Components)

	um11.step = "prepare"
	um11.continueChan <- true
	<-um11.notifyTestChan // receive prepare
	um11.sendState(pb.UmState_PREPARED)

	um12Components = append(um12Components, &pb.SystemComponent{Id: "um12C1", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um12Components = append(um12Components, &pb.SystemComponent{Id: "um12C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um12.setComponents(um12Components)

	um12.step = "prepare"
	um12.continueChan <- true
	<-um12.notifyTestChan
	um12.sendState(pb.UmState_PREPARED)

	um11.step = "update"
	um11.continueChan <- true
	<-um11.notifyTestChan //um11 updated
	um11.sendState(pb.UmState_UPDATED)

	um12Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um12C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um12C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um12C2", VendorVersion: "2", Status: pb.ComponentStatus_ERROR}}
	um12.setComponents(um12Components)

	um12.step = "update"
	um12.continueChan <- true
	<-um12.notifyTestChan //um12 updated
	// um12  reboot
	um12.step = "reboot"
	um12.closeConnection()
	<-um12.notifyTestChan

	um12 = newTestUM("testUM12", pb.UmState_FAILED, "revert", um12Components, t)
	go um12.processMessages()

	um11Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um11C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um11C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}
	um11.setComponents(um11Components)

	um11.step = "revert"
	um11.continueChan <- true
	<-um11.notifyTestChan //um11 revert received
	um11.sendState(pb.UmState_IDLE)

	um12Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um12C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um12C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um12C2", VendorVersion: "2", Status: pb.ComponentStatus_ERROR}}

	um12.step = "revert"
	um12.continueChan <- true
	<-um12.notifyTestChan //um12 revert received
	um12.sendState(pb.UmState_IDLE)

	um11.step = "finish"
	um12.step = "finish"

	<-finishChanel

	um11.closeConnection()
	um12.closeConnection()

	<-um11.notifyTestChan
	<-um12.notifyTestChan

	umCtrl.Close()

	etalonComponents := []amqp.ComponentInfo{amqp.ComponentInfo{ID: "um11C1", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um11C2", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um12C1", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um12C2", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um12C2", VendorVersion: "2", Status: "error"}}

	if !reflect.DeepEqual(etalonComponents, updateSender.Components) {
		log.Debug(updateSender.Components)
		t.Error("incorrect result component list")
		log.Debug(etalonComponents)
	}

	time.Sleep(time.Second)
}

func TestRevertOnUpdateWithReboot(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	umCtrlConfig := config.UmController{
		ServerURL: "localhost:8091",
		UmClients: []config.UmClientConfig{
			config.UmClientConfig{UmID: "testUM13", Priority: 1, IsLocal: true},
			config.UmClientConfig{UmID: "testUM14", Priority: 10}},
		UpdateDir:     tmpDir,
		FileServerURL: "localhost:8099",
	}

	smConfig := config.Config{UmController: umCtrlConfig}

	var updateSender testStatusSender
	var updateStorage testStorage

	updateDownloader := testNotifyDownloader{downloadSyncCh: make(chan bool, 1), componetCount: 3}

	umCtrl, err := umcontroller.New(&smConfig, &updateSender, &updateStorage, &updateDownloader, true)
	if err != nil {
		t.Errorf("Can't create: UM controller %s", err)
	}

	um13Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um13C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um13C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um13 := newTestUM("testUM13", pb.UmState_IDLE, "init", um13Components, t)
	go um13.processMessages()

	um14Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um14C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um14C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um14 := newTestUM("testUM14", pb.UmState_IDLE, "init", um14Components, t)
	go um14.processMessages()

	desiredStatus := []amqp.ComponentInfoFromCloud{amqp.ComponentInfoFromCloud{ID: "um13C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "1"}},
		amqp.ComponentInfoFromCloud{ID: "um13C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um14C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um14C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
	}

	finishChanel := make(chan bool)

	go func() {
		umCtrl.ProcessDesiredComponents(desiredStatus, []amqp.CertificateChain{}, []amqp.Certificate{})
		close(finishChanel)
	}()

	<-updateDownloader.downloadSyncCh

	um13Components = append(um13Components, &pb.SystemComponent{Id: "um13C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um13.setComponents(um13Components)

	um13.step = "prepare"
	um13.continueChan <- true
	<-um13.notifyTestChan // receive prepare
	um13.sendState(pb.UmState_PREPARED)

	um14Components = append(um14Components, &pb.SystemComponent{Id: "um14C1", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um14Components = append(um14Components, &pb.SystemComponent{Id: "um14C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um14.setComponents(um14Components)

	um14.step = "prepare"
	um14.continueChan <- true
	<-um14.notifyTestChan
	um14.sendState(pb.UmState_PREPARED)

	um13.step = "update"
	um13.continueChan <- true
	<-um13.notifyTestChan //um13 updated
	um13.sendState(pb.UmState_UPDATED)

	um14Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um14C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um14C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um14C2", VendorVersion: "2", Status: pb.ComponentStatus_ERROR}}
	um14.setComponents(um14Components)

	um14.step = "update"
	um14.continueChan <- true
	<-um14.notifyTestChan //um14 updated

	// full reboot
	um13.step = "reboot"
	um14.step = "reboot"

	um13.closeConnection()
	um14.closeConnection()
	umCtrl.Close()

	<-um13.notifyTestChan
	<-um14.notifyTestChan
	<-finishChanel
	// um14  reboot

	umCtrl, err = umcontroller.New(&smConfig, &updateSender, &updateStorage, &updateDownloader, true)
	if err != nil {
		t.Errorf("Can't create: UM controller %s", err)
	}

	um13 = newTestUM("testUM13", pb.UmState_UPDATED, "revert", um13Components, t)
	go um13.processMessages()

	um14 = newTestUM("testUM14", pb.UmState_FAILED, "revert", um14Components, t)
	go um14.processMessages()

	um13Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um13C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um13C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}
	um13.setComponents(um13Components)

	um13.step = "revert"
	um13.continueChan <- true
	<-um13.notifyTestChan //um13 revert received
	um13.sendState(pb.UmState_IDLE)

	um14Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um14C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um14C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um14C2", VendorVersion: "2", Status: pb.ComponentStatus_ERROR}}

	um14.step = "revert"
	um14.continueChan <- true
	<-um14.notifyTestChan //um14 revert received
	um14.sendState(pb.UmState_IDLE)

	um13.step = "finish"
	um14.step = "finish"

	time.Sleep(time.Second)

	um13.closeConnection()
	um14.closeConnection()

	<-um13.notifyTestChan
	<-um14.notifyTestChan

	umCtrl.Close()

	etalonComponents := []amqp.ComponentInfo{amqp.ComponentInfo{ID: "um13C1", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um13C2", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um14C1", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um14C2", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um14C2", VendorVersion: "2", Status: "error"}}

	if !reflect.DeepEqual(etalonComponents, updateSender.Components) {
		log.Debug(updateSender.Components)
		t.Error("incorrect result component list")
		log.Debug(etalonComponents)
	}

	time.Sleep(time.Second)
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/
func (sender *testStatusSender) SendComponentStatus(components []amqp.ComponentInfo) {
	sender.Components = make([]amqp.ComponentInfo, len(components))
	copy(sender.Components, components)
}

func (storage *testStorage) GetComponentsUpdateInfo() (updateInfo []umcontroller.SystemComponent, err error) {
	return storage.updateInfo, err
}

func (storage *testStorage) SetComponentsUpdateInfo(updateInfo []umcontroller.SystemComponent) (err error) {
	storage.updateInfo = updateInfo
	return err
}

func (downloader *testDownloader) DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
	chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error) {

	return "", nil
}

func (downloader *testNotifyDownloader) DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
	chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error) {
	log.Debug("testNotifyDownloader")

	downloader.curretCount++
	if downloader.curretCount == downloader.componetCount {
		downloader.downloadSyncCh <- true
	}

	resultFile = path.Join(decryptDir, strconv.Itoa(downloader.curretCount))

	if err := ioutil.WriteFile(resultFile, []byte("Some Update"), 0644); err != nil {
		return resultFile, err
	}

	return resultFile, nil
}

func (um *testUmConnection) processMessages() {
	defer func() { um.notifyTestChan <- true }()
	for {
		<-um.continueChan
		msg, err := um.stream.Recv()
		switch um.step {
		case "finish":
			fallthrough

		case "reboot":
			if err == io.EOF {
				log.Debug("[test] End of connection ", um.umId)
				return
			}

			if err != nil {
				log.Debug("[test] End of connection with error ", err, um.umId)
				return
			}

		case "prepare":
			if msg.GetPrepareUpdate() == nil {
				um.test.Error("Expect prepare update request ", um.umId)
			}

		case "update":
			if msg.GetStartUpdate() == nil {
				um.test.Error("Expect start update ", um.umId)
			}

		case "apply":
			if msg.GetApplyUpdate() == nil {
				um.test.Error("Expect apply update ", um.umId)
			}

		case "revert":
			if msg.GetRevertUpdate() == nil {
				um.test.Error("Expect revert update ", um.umId)
			}

		default:
			um.test.Error("unexpected message at step", um.step)
		}
		um.notifyTestChan <- true
	}
}

/*******************************************************************************
 * Private
 ******************************************************************************/
func newTestUM(id string, umState pb.UmState, testState string, components []*pb.SystemComponent, t *testing.T) (umTest *testUmConnection) {
	stream, conn, err := createClientConnection(id, umState, components)
	if err != nil {
		t.Errorf("Error connect %s", err)
		return umTest
	}

	umTest = &testUmConnection{
		notifyTestChan: make(chan bool),
		continueChan:   make(chan bool),
		step:           testState,
		test:           t,
		stream:         stream,
		umId:           id,
		conn:           conn,
		components:     components,
	}

	return umTest
}

func (um *testUmConnection) setComponents(components []*pb.SystemComponent) {
	um.components = components
}

func (um *testUmConnection) sendState(state pb.UmState) {
	umMsg := &pb.UpdateStatus{UmId: um.umId, UmState: state, Components: um.components}

	if err := um.stream.Send(umMsg); err != nil {
		um.test.Errorf("Fail send update status message %s", err)
	}
}

func (um *testUmConnection) closeConnection() {
	um.continueChan <- true
	um.conn.Close()
	um.stream.CloseSend()
}
