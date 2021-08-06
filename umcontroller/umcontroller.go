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

package umcontroller

import (
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/looplab/fsm"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
	"gitpct.epam.com/epmd-aepr/aos_common/image"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/downloader"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// UmController update managers controller
type UmController struct {
	sender       statusSender
	storage      storage
	downloader   Downloader
	server       *umCtrlServer
	eventChannel chan umCtrlInternalMsg
	stopChannel  chan bool

	updateDir string

	connections       []umConnection
	currentComponents []amqp.ComponentInfo
	fsm               *fsm.FSM
	connectionMonitor allConnectionMonitor
	fileStorage       *fileStorage
	operable          bool
	updateFinishCond  *sync.Cond
}

// SystemComponent infromation about system component update
type SystemComponent struct {
	ID            string `json:"id"`
	VendorVersion string `json:"vendorVersion"`
	AosVersion    uint64 `json:"aosVersion"`
	Annotations   string `json:"annotations,omitempty"`
	URL           string `json:"url"`
	Sha256        []byte `json:"sha256"`
	Sha512        []byte `json:"sha512"`
	Size          uint64 `json:"size"`
}

type umConnection struct {
	umID           string
	isLocalClient  bool
	handler        *umHandler
	updatePriority uint32
	state          string
	components     []string
	updatePackages []SystemComponent
}

type umCtrlInternalMsg struct {
	umID        string
	handler     *umHandler
	requestType int
	status      umStatus
}

type umStatus struct {
	umState       string
	componsStatus []systemComponentStatus
}

type systemComponentStatus struct {
	id            string
	vendorVersion string
	aosVersion    uint64
	status        string
	err           string
}

type updateRequest struct {
	components []amqp.ComponentInfoFromCloud
	chains     []amqp.CertificateChain
	certs      []amqp.Certificate
}

type allConnectionMonitor struct {
	connTimer     *time.Timer
	timeoutChan   chan bool
	stopTimerChan chan bool
	wg            sync.WaitGroup
}

type statusSender interface {
	SendComponentStatus(components []amqp.ComponentInfo)
}

type storage interface {
	GetComponentsUpdateInfo() (updateInfo []SystemComponent, err error)
	SetComponentsUpdateInfo(updateInfo []SystemComponent) (err error)
}

type Downloader interface {
	DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
		chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error)
}

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	openConnection = iota
	closeConnection
	umStatusUpdate
)

// Component status
const (
	StatusPending     = "pending"
	StatusDownloading = "downloading"
	StatusDownloaded  = "downloaded"
	StatusInstalling  = "installing"
	StatusInstalled   = "installed"
	StatusError       = "error"
)

// FSM states
const (
	stateInit                          = "init"
	stateIdle                          = "idle"
	stateFaultState                    = "fault"
	stateDownloading                   = "downloading"
	statePrepareUpdate                 = "prepareUpdate"
	stateUpdateUmStatusOnPrepareUpdate = "updateUmStatusOnPrepareUpdate"
	stateStartUpdate                   = "startUpdate"
	stateUpdateUmStatusOnStartUpdate   = "updateUmStatusOnStartUpdate"
	stateStartApply                    = "startApply"
	stateUpdateUmStatusOnStartApply    = "updateUmStatusOnStartApply"
	stateStartRevert                   = "startRevert"
	stateUpdateUmStatusOnRevert        = "updateUmStatusOnRevert"
)

// FSM events
const (
	evAllClientsConnected = "allClientsConnected"
	evConnectionTimeout   = "connectionTimeout"
	evUpdateRequest       = "updateRequest"
	evDownloadSuccess     = "downloadSuccess"
	evContinue            = "continue"
	evUpdatePrepared      = "updatePrepared"
	evUmStateUpdated      = "umStateUpdated"
	evSystemUpdated       = "systemUpdated"
	evApplyComplete       = "applyComplete"

	evContinuePrepare = "continuePrepare"
	evContinueUpdate  = "continueUpdate"
	evContinueApply   = "continueApply"
	evContinueRevert  = "continueRevert"

	evUpdateFailed   = "updateFailed"
	evSystemReverted = "systemReverted"
)

// client sates
const (
	umIdle     = "IDLE"
	umPrepared = "PREPARED"
	umUpdated  = "UPDATED"
	umFailed   = "FAILED"
)

const connectionTimeoutSec = 300

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new update managers controller
func New(config *config.Config, sender statusSender, storage storage,
	downloader Downloader, insecure bool) (umCtrl *UmController, err error) {
	umCtrl = &UmController{
		sender:            sender,
		storage:           storage,
		downloader:        downloader,
		updateDir:         config.UmController.UpdateDir,
		eventChannel:      make(chan umCtrlInternalMsg),
		stopChannel:       make(chan bool),
		connectionMonitor: allConnectionMonitor{stopTimerChan: make(chan bool, 1), timeoutChan: make(chan bool, 1)},
		operable:          true,
		updateFinishCond:  sync.NewCond(&sync.Mutex{}),
	}

	if umCtrl.fileStorage, err = newFileStorage(config.UmController); err != nil {
		return nil, aoserrors.Wrap(err)
	}
	for _, client := range config.UmController.UmClients {
		umCtrl.connections = append(umCtrl.connections, umConnection{umID: client.UmID,
			isLocalClient: client.IsLocal, updatePriority: client.Priority, handler: nil})
	}

	sort.Slice(umCtrl.connections, func(i, j int) bool {
		return umCtrl.connections[i].updatePriority < umCtrl.connections[j].updatePriority
	})

	umCtrl.fsm = fsm.NewFSM(
		stateInit,
		fsm.Events{
			//process Idle state
			{Name: evAllClientsConnected, Src: []string{stateInit}, Dst: stateIdle},
			{Name: evContinuePrepare, Src: []string{stateIdle}, Dst: statePrepareUpdate},
			{Name: evContinueUpdate, Src: []string{stateIdle}, Dst: stateStartUpdate},
			{Name: evContinueApply, Src: []string{stateIdle}, Dst: stateStartApply},
			{Name: evContinueRevert, Src: []string{stateIdle}, Dst: stateStartRevert},
			//process downloading
			{Name: evUpdateRequest, Src: []string{stateIdle}, Dst: stateDownloading},
			{Name: evUpdateFailed, Src: []string{stateDownloading}, Dst: stateIdle},
			//process prepare
			{Name: evDownloadSuccess, Src: []string{stateDownloading}, Dst: statePrepareUpdate},
			{Name: evUmStateUpdated, Src: []string{statePrepareUpdate}, Dst: stateUpdateUmStatusOnPrepareUpdate},
			{Name: evContinue, Src: []string{stateUpdateUmStatusOnPrepareUpdate}, Dst: statePrepareUpdate},
			//process start update
			{Name: evUpdatePrepared, Src: []string{statePrepareUpdate}, Dst: stateStartUpdate},
			{Name: evUmStateUpdated, Src: []string{stateStartUpdate}, Dst: stateUpdateUmStatusOnStartUpdate},
			{Name: evContinue, Src: []string{stateUpdateUmStatusOnStartUpdate}, Dst: stateStartUpdate},
			//process start apply
			{Name: evSystemUpdated, Src: []string{stateStartUpdate}, Dst: stateStartApply},
			{Name: evUmStateUpdated, Src: []string{stateStartApply}, Dst: stateUpdateUmStatusOnStartApply},
			{Name: evContinue, Src: []string{stateUpdateUmStatusOnStartApply}, Dst: stateStartApply},
			{Name: evApplyComplete, Src: []string{stateStartApply}, Dst: stateIdle},
			//process revert
			{Name: evUpdateFailed, Src: []string{statePrepareUpdate}, Dst: stateStartRevert},
			{Name: evUpdateFailed, Src: []string{stateStartUpdate}, Dst: stateStartRevert},
			{Name: evUpdateFailed, Src: []string{stateStartApply}, Dst: stateStartRevert},
			{Name: evUmStateUpdated, Src: []string{stateStartRevert}, Dst: stateUpdateUmStatusOnRevert},
			{Name: evContinue, Src: []string{stateUpdateUmStatusOnRevert}, Dst: stateStartRevert},
			{Name: evSystemReverted, Src: []string{stateStartRevert}, Dst: stateIdle},

			{Name: evConnectionTimeout, Src: []string{stateInit}, Dst: stateFaultState},
		},
		fsm.Callbacks{
			"enter_" + stateIdle:                          umCtrl.processIdleState,
			"enter_" + stateDownloading:                   umCtrl.processNewComponentList,
			"enter_" + statePrepareUpdate:                 umCtrl.processPrepareState,
			"enter_" + stateUpdateUmStatusOnPrepareUpdate: umCtrl.processUpdateUmState,
			"enter_" + stateStartUpdate:                   umCtrl.processStartUpdateState,
			"enter_" + stateUpdateUmStatusOnStartUpdate:   umCtrl.processUpdateUmState,
			"enter_" + stateStartApply:                    umCtrl.processStartApplyState,
			"enter_" + stateUpdateUmStatusOnStartApply:    umCtrl.processUpdateUmState,
			"enter_" + stateStartRevert:                   umCtrl.processStartRevertState,
			"enter_" + stateUpdateUmStatusOnRevert:        umCtrl.processUpdateUmState,
			"enter_" + stateFaultState:                    umCtrl.processFaultState,

			"before_event":               umCtrl.onEvent,
			"before_" + evApplyComplete:  umCtrl.updateComplete,
			"before_" + evSystemReverted: umCtrl.revertComplete,
			"before_" + evUpdateFailed:   umCtrl.processError,
		},
	)

	umCtrl.server, err = newServer(config, umCtrl.eventChannel, insecure)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	go umCtrl.processInternallMessages()
	go umCtrl.connectionMonitor.startConnectionTimer(len(umCtrl.connections))
	go umCtrl.server.Start()
	go umCtrl.fileStorage.startFileStorage()

	return umCtrl, nil
}

// Close close server
func (umCtrl *UmController) Close() {
	umCtrl.operable = false
	umCtrl.stopChannel <- true
}

// GetSystemComponents returns list of system components information
func (umCtrl *UmController) GetSystemComponents() (components []amqp.ComponentInfo, err error) {
	currentState := umCtrl.fsm.Current()
	if currentState != stateInit {
		return umCtrl.currentComponents, nil
	}

	umCtrl.connectionMonitor.wg.Wait()

	return umCtrl.currentComponents, nil
}

// ProcessDesiredComponents process desred component list
func (umCtrl *UmController) ProcessDesiredComponents(components []amqp.ComponentInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (err error) {
	currentState := umCtrl.fsm.Current()
	if currentState == stateIdle {
		newComponents := []amqp.ComponentInfoFromCloud{}

		for _, desComponent := range components {
			wasFound := false

			for _, curComponent := range umCtrl.currentComponents {
				if curComponent.ID != desComponent.ID {
					continue
				}

				if curComponent.VendorVersion == desComponent.VendorVersion &&
					curComponent.Status != StatusError {
					wasFound = true
				}

				break
			}

			if wasFound == false {
				newComponents = append(newComponents, desComponent)
				umCtrl.updateComponentElement(systemComponentStatus{id: desComponent.ID,
					vendorVersion: desComponent.VendorVersion,
					aosVersion:    desComponent.AosVersion,
					status:        StatusPending,
				})
			}
		}

		if len(newComponents) == 0 {
			log.Debug("No update for componenets")
			return nil
		}

		umCtrl.generateFSMEvent(evUpdateRequest, updateRequest{components: newComponents, certs: certs, chains: chains})
	} else {
		log.Debug("Another update in progress, state =", currentState)
	}

	umCtrl.updateFinishCond.L.Lock()
	umCtrl.updateFinishCond.Wait()
	umCtrl.updateFinishCond.L.Unlock()

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (umCtrl *UmController) processInternallMessages() {
	for {
		select {
		case <-umCtrl.connectionMonitor.timeoutChan:
			if len(umCtrl.connections) == 0 {
				umCtrl.generateFSMEvent(evAllClientsConnected)
			} else {
				log.Error("Ums connection timeout")
				umCtrl.generateFSMEvent(evConnectionTimeout)
			}

		case internalMsg := <-umCtrl.eventChannel:
			log.Debug("Internal Event ", internalMsg.requestType)

			switch internalMsg.requestType {
			case openConnection:
				umCtrl.handleNewConnection(internalMsg.umID, internalMsg.handler, internalMsg.status)

			case closeConnection:
				umCtrl.handleCloseConnection(internalMsg.umID)

			case umStatusUpdate:
				umCtrl.generateFSMEvent(evUmStateUpdated, internalMsg.umID, internalMsg.status)

			default:
				log.Error("Unsupported internal message ", internalMsg.requestType)
			}

		case <-umCtrl.stopChannel:
			log.Debug("Close all connections")

			umCtrl.server.Stop()
			umCtrl.fileStorage.stopFileStorage()
			umCtrl.updateFinishCond.Broadcast()

			return
		}
	}
}

func (umCtrl *UmController) handleNewConnection(umID string, handler *umHandler, status umStatus) {
	if handler == nil {
		log.Error("Handler is nil")
		return
	}

	umIDfound := false
	for i, value := range umCtrl.connections {
		if value.umID != umID {
			continue
		}

		umCtrl.updateCurrentComponetsStatus(status.componsStatus)

		umIDfound = true

		if value.handler != nil {
			log.Warn("Connection already availabe umID = ", umID)
			value.handler.Close()
		}

		umCtrl.connections[i].handler = handler
		umCtrl.connections[i].state = handler.GetInitilState()
		umCtrl.connections[i].components = []string{}

		for _, newComponent := range status.componsStatus {
			idExist := false
			for _, value := range umCtrl.connections[i].components {
				if value == newComponent.id {
					idExist = true
					break
				}
			}

			if idExist == true {
				continue
			}

			umCtrl.connections[i].components = append(umCtrl.connections[i].components, newComponent.id)
		}

		break

	}

	if umIDfound == false {
		log.Error("Unexpected new UM connection with ID = ", umID)
		handler.Close()
		return
	}

	for _, value := range umCtrl.connections {
		if value.handler == nil {
			return
		}
	}

	log.Debug("All connection to Ums established")

	umCtrl.connectionMonitor.stopConnectionTimer()

	if err := umCtrl.getUpdateComponentsFromStorage(); err != nil {
		log.Error("Can't read update components from storage: ", err)
	}

	umCtrl.generateFSMEvent(evAllClientsConnected)

	return
}

func (umCtrl *UmController) handleCloseConnection(umID string) {
	log.Debug("Close UM connection umid = ", umID)
	for i, value := range umCtrl.connections {
		if value.umID == umID {
			umCtrl.connections[i].handler = nil

			umCtrl.fsm.SetState(stateInit)

			go umCtrl.connectionMonitor.startConnectionTimer(len(umCtrl.connections))

			return
		}
	}
}

func (umCtrl *UmController) updateCurrentComponetsStatus(componsStatus []systemComponentStatus) {
	log.Debug("Receive components: ", componsStatus)
	for _, value := range componsStatus {
		if value.status == StatusInstalled {
			toRemove := []int{}

			for i, curStatus := range umCtrl.currentComponents {
				if value.id == curStatus.ID {
					if curStatus.Status != StatusInstalled {
						continue
					}

					if value.vendorVersion != curStatus.VendorVersion {
						toRemove = append(toRemove, i)
						continue
					}
				}
			}

			sort.Ints(toRemove)

			for i, value := range toRemove {
				umCtrl.currentComponents = append(umCtrl.currentComponents[:value-i],
					umCtrl.currentComponents[value-i+1:]...)
			}
		}

		umCtrl.updateComponentElement(value)
	}
}

func (umCtrl *UmController) updateComponentElement(component systemComponentStatus) {
	componentExist := false
	for i, curElement := range umCtrl.currentComponents {
		if curElement.ID == component.id && curElement.VendorVersion == component.vendorVersion {
			umCtrl.currentComponents[i].Status = component.status
			umCtrl.currentComponents[i].Error = component.err
			umCtrl.sender.SendComponentStatus(umCtrl.currentComponents)

			componentExist = true

			break
		}
	}

	if componentExist == false {
		umCtrl.currentComponents = append(umCtrl.currentComponents, amqp.ComponentInfo{
			ID:            component.id,
			VendorVersion: component.vendorVersion,
			AosVersion:    component.aosVersion,
			Status:        component.status,
			Error:         component.err,
		})
	}

	umCtrl.sender.SendComponentStatus(umCtrl.currentComponents)

	return
}

func (umCtrl *UmController) cleanupCurrentCompontStatus() {
	toRemove := []int{}

	for i, value := range umCtrl.currentComponents {
		if value.Status != StatusInstalled && value.Status != StatusError {
			toRemove = append(toRemove, i)
		}
	}

	if len(toRemove) == 0 {
		return
	}

	for i, value := range toRemove {
		umCtrl.currentComponents = append(umCtrl.currentComponents[:value-i], umCtrl.currentComponents[value-i+1:]...)

	}

	umCtrl.sender.SendComponentStatus(umCtrl.currentComponents)
}

func (umCtrl *UmController) downloadComponentUpdate(componentUpdate amqp.ComponentInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (infoForUpdate SystemComponent, err error) {
	decryptData := amqp.DecryptDataStruct{URLs: componentUpdate.URLs,
		Sha256:         componentUpdate.Sha256,
		Sha512:         componentUpdate.Sha512,
		Size:           componentUpdate.Size,
		DecryptionInfo: componentUpdate.DecryptionInfo,
		Signs:          componentUpdate.Signs}

	infoForUpdate = SystemComponent{ID: componentUpdate.ID,
		VendorVersion: componentUpdate.VendorVersion,
		AosVersion:    componentUpdate.AosVersion,
		Annotations:   string(componentUpdate.Annotations),
	}

	infoForUpdate.URL, err = umCtrl.downloader.DownloadAndDecrypt(decryptData, chains, certs, umCtrl.updateDir)
	if err != nil {
		return infoForUpdate, aoserrors.Wrap(err)
	}

	fileInfo, err := image.CreateFileInfo(infoForUpdate.URL)
	if err != nil {
		return infoForUpdate, aoserrors.Wrap(err)
	}

	infoForUpdate.Sha256 = fileInfo.Sha256
	infoForUpdate.Sha512 = fileInfo.Sha512
	infoForUpdate.Size = fileInfo.Size

	return infoForUpdate, aoserrors.Wrap(err)
}

func (umCtrl *UmController) getCurrentUpdateState() (state string) {
	var onPrepareState, onApplyState bool

	for _, conn := range umCtrl.connections {
		switch conn.state {
		case umFailed:
			return stateFaultState

		case umPrepared:
			onPrepareState = true

		case umUpdated:
			onApplyState = true

		default:
			continue

		}
	}

	if onPrepareState == true {
		return statePrepareUpdate
	}

	if onApplyState == true {
		return stateStartApply
	}

	return stateIdle
}

func (umCtrl *UmController) getUpdateComponentsFromStorage() (err error) {
	for i := range umCtrl.connections {
		umCtrl.connections[i].updatePackages = []SystemComponent{}
	}

	updatecomponents, err := umCtrl.storage.GetComponentsUpdateInfo()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, component := range updatecomponents {
		umCtrl.addComponentForUpdateToUm(component)
	}

	return nil
}

func (umCtrl *UmController) addComponentForUpdateToUm(componentInfo SystemComponent) (added bool) {
	for i := range umCtrl.connections {
		for _, id := range umCtrl.connections[i].components {
			if id == componentInfo.ID {
				componentInfo.URL = umCtrl.fileStorage.getImageURL(umCtrl.connections[i].isLocalClient,
					componentInfo.URL)

				umCtrl.connections[i].updatePackages = append(umCtrl.connections[i].updatePackages, componentInfo)
				return true
			}
		}
	}

	return false
}

func (umCtrl *UmController) cleanupUpdateData() {
	for i := range umCtrl.connections {
		umCtrl.connections[i].updatePackages = []SystemComponent{}
	}

	os.RemoveAll(umCtrl.updateDir)

	updatecomponents, err := umCtrl.storage.GetComponentsUpdateInfo()
	if err != nil {
		log.Error("Can't get components update info ", err)
		return
	}

	if len(updatecomponents) == 0 {
		return
	}

	if err := umCtrl.storage.SetComponentsUpdateInfo([]SystemComponent{}); err != nil {
		log.Error("Can't clean components update info ", err)
	}

	return
}

func (umCtrl *UmController) generateFSMEvent(event string, args ...interface{}) (err error) {
	if umCtrl.operable == false {
		return aoserrors.New("update controller in shutdown state")
	}

	err = umCtrl.fsm.Event(event, args...)
	if err != nil {
		log.Error("Error transaction ", err)
	}

	return aoserrors.Wrap(err)
}

func (monitor *allConnectionMonitor) startConnectionTimer(connectionsCount int) {
	if connectionsCount == 0 {
		monitor.timeoutChan <- true
		return
	}

	if monitor.connTimer != nil {
		log.Debug("Timer already started")
		return
	}

	monitor.wg.Add(1)
	defer monitor.wg.Done()

	monitor.connTimer = time.NewTimer(connectionTimeoutSec * time.Second)

	select {
	case <-monitor.connTimer.C:
		monitor.timeoutChan <- true

	case <-monitor.stopTimerChan:
		monitor.connTimer.Stop()
	}

	monitor.connTimer = nil

	return
}

func (monitor *allConnectionMonitor) stopConnectionTimer() {
	monitor.stopTimerChan <- true
}

/*******************************************************************************
 * FSM callbacks
 ******************************************************************************/

func (umCtrl *UmController) onEvent(e *fsm.Event) {
	log.Infof("[CtrlFSM] %s -> %s : Event: %s", e.Src, e.Dst, e.Event)
}

func (umCtrl *UmController) processIdleState(e *fsm.Event) {
	umState := umCtrl.getCurrentUpdateState()

	switch umState {
	case stateFaultState:
		go umCtrl.generateFSMEvent(evContinueRevert)
		return

	case statePrepareUpdate:
		go umCtrl.generateFSMEvent(evContinuePrepare)
		return

	case stateStartApply:
		go umCtrl.generateFSMEvent(evContinueApply)
		return
	}

	umCtrl.updateFinishCond.Broadcast()
	umCtrl.cleanupUpdateData()
}

func (umCtrl *UmController) processFaultState(e *fsm.Event) {

}

func (umCtrl *UmController) processNewComponentList(e *fsm.Event) {
	newComponentList := e.Args[0].(updateRequest)

	if err := os.MkdirAll(umCtrl.updateDir, 755); err != nil {
		go umCtrl.generateFSMEvent(evUpdateFailed, err.Error())
		return
	}

	componentsToSave := []SystemComponent{}

	for _, component := range newComponentList.components {
		componentStatus := systemComponentStatus{id: component.ID, vendorVersion: component.VendorVersion,
			aosVersion: component.AosVersion, status: StatusDownloading}

		umCtrl.updateComponentElement(componentStatus)
		updatePackage, err := umCtrl.downloadComponentUpdate(component, newComponentList.chains, newComponentList.certs)

		if err != nil {
			// Do not return error if can't download file. It will be downloaded again on next desired status
			// Just display error
			if err == downloader.ErrNotDownloaded {
				log.WithFields(log.Fields{"id": component.ID}).Errorf("Can't download component image: %s", aoserrors.Wrap(err))
				return
			}

			componentStatus.status = StatusError
			componentStatus.err = err.Error()
			umCtrl.updateComponentElement(componentStatus)

			go umCtrl.generateFSMEvent(evUpdateFailed, err.Error())
			return
		}

		if umCtrl.addComponentForUpdateToUm(updatePackage) == false {
			log.Warn("Downloaded unsupported component ", updatePackage.ID)
			continue
		}

		componentsToSave = append(componentsToSave, updatePackage)

		componentStatus.status = StatusDownloaded
		umCtrl.updateComponentElement(componentStatus)
	}

	if err := umCtrl.storage.SetComponentsUpdateInfo(componentsToSave); err != nil {
		go umCtrl.generateFSMEvent(evUpdateFailed, err.Error())
		return
	}

	go umCtrl.generateFSMEvent(evDownloadSuccess)
}

func (umCtrl *UmController) processPrepareState(e *fsm.Event) {
	for i := range umCtrl.connections {
		if len(umCtrl.connections[i].updatePackages) > 0 {
			if umCtrl.connections[i].state == umFailed {
				go umCtrl.generateFSMEvent(evUpdateFailed, "preparUpdate failure umID = "+umCtrl.connections[i].umID)
				return
			}

			if umCtrl.connections[i].handler == nil {
				log.Warnf("Connection to um %s closed", umCtrl.connections[i].umID)
				return
			}

			if err := umCtrl.connections[i].handler.PrepareUpdate(umCtrl.connections[i].updatePackages); err == nil {
				return
			}
		}
	}

	go umCtrl.generateFSMEvent(evUpdatePrepared)
}

func (umCtrl *UmController) processStartUpdateState(e *fsm.Event) {
	log.Debug("processStartUpdateState")
	for i := range umCtrl.connections {
		if len(umCtrl.connections[i].updatePackages) > 0 {
			if umCtrl.connections[i].state == umFailed {
				go umCtrl.generateFSMEvent(evUpdateFailed, "update failure umID = "+umCtrl.connections[i].umID)
				return
			}
		}

		if umCtrl.connections[i].handler == nil {
			log.Warnf("Connection to um %s closed", umCtrl.connections[i].umID)
			return
		}

		if err := umCtrl.connections[i].handler.StartUpdate(); err == nil {
			return
		}
	}

	go umCtrl.generateFSMEvent(evSystemUpdated)
}

func (umCtrl *UmController) processStartRevertState(e *fsm.Event) {
	errAvailable := false

	for i := range umCtrl.connections {
		log.Debug(len(umCtrl.connections[i].updatePackages))
		if len(umCtrl.connections[i].updatePackages) > 0 || umCtrl.connections[i].state == umFailed {
			if umCtrl.connections[i].handler == nil {
				log.Warnf("Connection to um %s closed", umCtrl.connections[i].umID)
				return
			}

			if len(umCtrl.connections[i].updatePackages) == 0 {
				log.Warnf("No update components but UM %s is in failure state", umCtrl.connections[i].umID)
			}

			if err := umCtrl.connections[i].handler.StartRevert(); err == nil {
				return
			}

			if umCtrl.connections[i].state == umFailed {
				errAvailable = true
			}
		}
	}

	if errAvailable == true {
		log.Error("Maintain need") //todo think about cyclic  revert
		return
	}

	go umCtrl.generateFSMEvent(evSystemReverted)
}

func (umCtrl *UmController) processStartApplyState(e *fsm.Event) {
	for i := range umCtrl.connections {
		if len(umCtrl.connections[i].updatePackages) > 0 {
			if umCtrl.connections[i].state == umFailed {
				go umCtrl.generateFSMEvent(evUpdateFailed, "apply failure umID = "+umCtrl.connections[i].umID)
				return
			}
		}

		if umCtrl.connections[i].handler == nil {
			log.Warnf("Connection to um %s closed", umCtrl.connections[i].umID)
			return
		}

		if err := umCtrl.connections[i].handler.StartApply(); err == nil {
			return
		}
	}

	go umCtrl.generateFSMEvent(evApplyComplete)
}

func (umCtrl *UmController) processUpdateUmState(e *fsm.Event) {
	log.Debug("processUpdateUmState")
	umID := e.Args[0].(string)
	status := e.Args[1].(umStatus)

	for i, v := range umCtrl.connections {
		if v.umID == umID {
			umCtrl.connections[i].state = status.umState
			log.Debugf("UMid = %s  state= %s", umID, status.umState)
			break
		}
	}

	umCtrl.updateCurrentComponetsStatus(status.componsStatus)

	go umCtrl.generateFSMEvent(evContinue)
}

func (umCtrl *UmController) processError(e *fsm.Event) {
	errtorMSg := e.Args[0].(string)
	log.Error("update Error: ", errtorMSg)
}

func (umCtrl *UmController) revertComplete(e *fsm.Event) {
	log.Debug("Revert complete")

	umCtrl.cleanupCurrentCompontStatus()
}

func (umCtrl *UmController) updateComplete(e *fsm.Event) {
	log.Debug("Update finished")

	umCtrl.cleanupCurrentCompontStatus()
}

func (status systemComponentStatus) String() string {
	return fmt.Sprintf("{id: %s, status: %s, vendorVersion: %s aosVersion: %d }",
		status.id, status.status, status.vendorVersion, status.aosVersion)
}
