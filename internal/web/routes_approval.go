package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/morphis/gummi/internal/web/push"
	"github.com/morphis/gummi/internal/webapi"
)

// The routes a page at the board answers a waiting device with
// (approval.go). They are s.api routes: only a device that has the board
// may let another in, and no route or command answers one otherwise.
func (s *Server) approvalRoutes() {
	s.api("GET /api/devices/pending", s.handlePendingDevices)
	s.api("POST /api/devices/{id}/approve", s.handleApprove)
	s.api("POST /api/devices/{id}/reject", s.handleReject)
}

// pendingLeft is how many seconds device id has left to be let in.
func (s *Server) pendingLeft(id string) int {
	for _, d := range s.opt.Devices.Pending() {
		if d.ID == id {
			return max(0, int(d.PairedAt.Add(pendingTTL).Sub(s.now()).Seconds()))
		}
	}
	return 0
}

// handlePendingDevices is GET /api/devices/pending.
func (s *Server) handlePendingDevices(w http.ResponseWriter, r *http.Request) {
	out := webapi.PendingDevices{Devices: []webapi.PendingDevice{}}
	if s.opt.OpenAccess {
		writeJSON(w, http.StatusOK, out)
		return
	}
	now := s.now()
	for _, d := range s.opt.Devices.Pending() {
		out.Devices = append(out.Devices, webapi.PendingDevice{
			ID:            d.ID,
			Person:        d.Person,
			Device:        d.Name,
			UserAgent:     d.UserAgent,
			Source:        d.Source,
			Code:          d.Via,
			Via:           CodeOrigin(d.Via).ViaPage(),
			Origin:        d.Origin,
			RequestedAt:   d.PairedAt,
			ExpiresInSecs: max(0, int(d.PairedAt.Add(pendingTTL).Sub(now).Seconds())),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleApprove is POST /api/devices/{id}/approve: a person at the board
// lets a waiting device in. It has the board from its next request; its
// page, told over its event stream, reloads into it.
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	dev, who, ok := s.decide(w, r, s.opt.Devices.Approve)
	if !ok {
		return
	}
	line := who.Person + " approved " + dev.Person + " on " + dev.Name
	s.opt.Log("web: %s on %s (%s) approved %s on %s (%s) from %s %s",
		who.Person, who.Device, who.DeviceID, dev.Person, dev.Name, dev.ID, dev.Source, CodeOrigin(dev.Via).Via())
	s.hub.publish(webapi.Change{
		Kind: webapi.ChangeToast, Except: dev.ID,
		Text: line + ". If that was a mistake, unpair it on the machine hosting the board: gummi web unpair " + dev.ID,
	})
	s.notifyDecision("New device approved", line, dev.ID)
	writeJSON(w, http.StatusOK, webapi.OK{OK: true})
}

// handleReject is POST /api/devices/{id}/reject: a person at the board
// turns a waiting device away. Its token is honoured by nothing from
// then on; its page, told over its event stream, says so.
func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	dev, who, ok := s.decide(w, r, s.opt.Devices.Reject)
	if !ok {
		return
	}
	line := who.Person + " rejected " + dev.Person + " on " + dev.Name
	s.opt.Log("web: %s on %s (%s) rejected %s on %s (%s) from %s %s",
		who.Person, who.Device, who.DeviceID, dev.Person, dev.Name, dev.ID, dev.Source, CodeOrigin(dev.Via).Via())
	s.hub.publish(webapi.Change{Kind: webapi.ChangeToast, Text: line})
	s.notifyDecision("New device rejected", line, dev.ID)
	writeJSON(w, http.StatusOK, webapi.OK{OK: true})
}

// decide runs one answer to a waiting device and tells every page —
// the banners go, and the waiting page learns where it stands.
func (s *Server) decide(w http.ResponseWriter, r *http.Request, answer func(id, by string) (Device, error)) (Device, Who, bool) {
	if s.opt.OpenAccess {
		writeError(w, http.StatusConflict, "this board is served without pairing")
		return Device{}, Who{}, false
	}
	who, _ := WhoFrom(r.Context())
	id := r.PathValue("id")
	dev, err := answer(id, who.Person+" on "+who.Device+" ("+who.DeviceID+")")
	switch {
	case errors.Is(err, ErrNoDevice):
		writeError(w, http.StatusNotFound, "no device "+id+" is waiting to be let in")
		return Device{}, Who{}, false
	case errors.Is(err, ErrNotPending):
		// answered on another page first, withdrawn, or lapsed: the
		// banner this came from is stale, so it is refreshed
		s.hub.publish(webapi.Change{Kind: webapi.ChangePairing, ID: id})
		writeError(w, http.StatusConflict, err.Error())
		return Device{}, Who{}, false
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return Device{}, Who{}, false
	}
	s.hub.publish(webapi.Change{Kind: webapi.ChangePairing, ID: dev.ID})
	return dev, who, true
}

// announceRequest tells everyone at the board that a device asks to be
// let in: a line in the terminal, the request on every open page (the
// page shows it until someone answers it or it lapses), and a
// notification on every subscribed device, since the person who must
// answer may not have the page open.
func (s *Server) announceRequest(dev Device) {
	via := CodeOrigin(dev.Via).Via()
	s.opt.Log("web: %s on %s (%s) from %s paired %s and waits to be let in — approve or reject it on a paired device's page within %s, or `gummi web unpair %s`",
		dev.Person, dev.Name, dev.ID, dev.Source, via, pendingTTL.Round(time.Minute), dev.ID)
	s.hub.publish(webapi.Change{Kind: webapi.ChangePairing, ID: dev.ID})
	if s.opt.Push != nil && !s.opt.OpenAccess {
		s.opt.Push.Notifier.Post(push.Message{
			Title: "Approve a new device?",
			Body:  dev.Person + " on " + dev.Name + " from " + dev.Source + " paired " + CodeOrigin(dev.Via).ViaPage() + ". Open the board to approve or reject it.",
			URL:   "/",
			Tag:   "pairing-" + dev.ID,
		})
	}
}

// notifyDecision pushes an answer to a request to every subscribed
// device, replacing the request's own notification.
func (s *Server) notifyDecision(title, body, id string) {
	if s.opt.Push != nil && !s.opt.OpenAccess {
		s.opt.Push.Notifier.Post(push.Message{Title: title, Body: body, URL: "/", Tag: "pairing-" + id})
	}
}
