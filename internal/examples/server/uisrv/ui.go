package uisrv

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"sync"
	"text/template"
	"time"

	"github.com/google/uuid"

	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var (
	htmlDir string
	srv     *http.Server
	opampCA = sync.OnceValue(func() string {
		p, err := os.ReadFile("../../certs/certs/ca.cert.pem")
		if err != nil {
			panic(err)
		}
		return string(p)
	})
)

var logger = log.New(log.Default().Writer(), "[UI] ", log.Default().Flags()|log.Lmsgprefix|log.Lmicroseconds)

func Start(rootDir string) {
	htmlDir = path.Join(rootDir, "uisrv/html")

	mux := http.NewServeMux()
	mux.HandleFunc("/", renderRoot)
	mux.HandleFunc("/agents/json", renderRootJSON)
	mux.HandleFunc("/agent", renderAgent)
	mux.HandleFunc("/agent/json", renderAgentJSON)
	mux.HandleFunc("/save_config", saveCustomConfigForInstance)
	mux.HandleFunc("/save_config/json", saveCustomConfigForInstanceJSON)
	mux.HandleFunc("/rotate_client_cert", rotateInstanceClientCert)
	mux.HandleFunc("/opamp_connection_settings", opampConnectionSettings)
	srv = &http.Server{
		Addr:    "0.0.0.0:4321",
		Handler: mux,
	}
	go srv.ListenAndServe()
}

func Shutdown() {
	srv.Shutdown(context.Background())
}

// getHostNameFromAgent extracts the "host.name" attribute value from an agent
func getHostNameFromAgent(agent *data.Agent) string {
	if agent == nil || agent.Status == nil || agent.Status.AgentDescription == nil {
		return "N/A"
	}

	for _, attr := range agent.Status.AgentDescription.NonIdentifyingAttributes {
		if attr.Key == "host.name" && attr.Value != nil && attr.Value.Value != nil {
			if stringVal, ok := attr.Value.Value.(*protobufs.AnyValue_StringValue); ok {
				return stringVal.StringValue
			}
		}
	}
	return "N/A"
}

func renderTemplate(w http.ResponseWriter, htmlTemplateFile string, data interface{}) {
	// Create template with custom functions
	t := template.New(htmlTemplateFile).Funcs(template.FuncMap{
		"getHostName": getHostNameFromAgent,
	})

	t, err := t.ParseFiles(
		path.Join(htmlDir, "header.html"),
		path.Join(htmlDir, htmlTemplateFile),
	)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logger.Printf("Error parsing html template %s: %v", htmlTemplateFile, err)
		return
	}

	err = t.Lookup(htmlTemplateFile).Execute(w, data)
	if err != nil {
		// It is too late to send an HTTP status code since content is already written.
		// We can just log the error.
		logger.Printf("Error writing html content %s: %v", htmlTemplateFile, err)
		return
	}
}

func renderRoot(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "root.html", data.AllAgents.GetAllAgentsReadonlyClone())
}

func renderRootJSON(w http.ResponseWriter, r *http.Request) {
	allAgents := data.AllAgents.GetAllAgentsReadonlyClone()

	// Convert agents map to a slice of AgentJSON for cleaner JSON output
	var agentsJSON []AgentJSON
	for _, agent := range allAgents {
		agentJSON := AgentJSON{
			InstanceId:                  uuid.UUID(agent.InstanceId).String(),
			InstanceIdStr:               agent.InstanceIdStr,
			Status:                      agent.Status,
			StartedAt:                   agent.StartedAt,
			EffectiveConfig:             agent.EffectiveConfig,
			CustomInstanceConfig:        agent.CustomInstanceConfig,
			ClientCertSha256Fingerprint: agent.ClientCertSha256Fingerprint,
			ClientCertOfferError:        agent.ClientCertOfferError,
		}
		agentsJSON = append(agentsJSON, agentJSON)
	}

	// Set Content-Type header for JSON response
	w.Header().Set("Content-Type", "application/json")

	// Encode agents to JSON and write to response
	if err := json.NewEncoder(w).Encode(agentsJSON); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logger.Printf("Error encoding agents to JSON: %v", err)
		return
	}
}

func renderAgent(w http.ResponseWriter, r *http.Request) {
	uid, err := uuid.Parse(r.URL.Query().Get("instanceid"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	agent := data.AllAgents.GetAgentReadonlyClone(data.InstanceId(uid))
	if agent == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	renderTemplate(w, "agent.html", agent)
}

// AgentJSON represents the Agent data structure for JSON serialization
type AgentJSON struct {
	InstanceId                  string                   `json:"instanceId"`
	InstanceIdStr               string                   `json:"instanceIdStr"`
	Status                      *protobufs.AgentToServer `json:"status"`
	StartedAt                   time.Time                `json:"startedAt"`
	EffectiveConfig             string                   `json:"effectiveConfig"`
	CustomInstanceConfig        string                   `json:"customInstanceConfig"`
	ClientCertSha256Fingerprint string                   `json:"clientCertSha256Fingerprint"`
	ClientCertOfferError        string                   `json:"clientCertOfferError"`
}

// ConfigUpdateResponse represents the response for config update operations
type ConfigUpdateResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	InstanceId  string `json:"instanceId"`
	StatusReady bool   `json:"statusReady"`
}

func renderAgentJSON(w http.ResponseWriter, r *http.Request) {
	uid, err := uuid.Parse(r.URL.Query().Get("instanceid"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	agent := data.AllAgents.GetAgentReadonlyClone(data.InstanceId(uid))
	if agent == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Create a JSON-safe representation of the agent
	agentJSON := AgentJSON{
		InstanceId:                  uuid.UUID(agent.InstanceId).String(),
		InstanceIdStr:               agent.InstanceIdStr,
		Status:                      agent.Status,
		StartedAt:                   agent.StartedAt,
		EffectiveConfig:             agent.EffectiveConfig,
		CustomInstanceConfig:        agent.CustomInstanceConfig,
		ClientCertSha256Fingerprint: agent.ClientCertSha256Fingerprint,
		ClientCertOfferError:        agent.ClientCertOfferError,
	}

	// Set Content-Type header for JSON response
	w.Header().Set("Content-Type", "application/json")

	// Encode agent to JSON and write to response
	if err := json.NewEncoder(w).Encode(agentJSON); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logger.Printf("Error encoding agent to JSON: %v", err)
		return
	}
}

func saveCustomConfigForInstance(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	uid, err := uuid.Parse(r.Form.Get("instanceid"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	instanceId := data.InstanceId(uid)
	agent := data.AllAgents.GetAgentReadonlyClone(instanceId)
	if agent == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	configStr := r.PostForm.Get("config")
	config := &protobufs.AgentConfigMap{
		ConfigMap: map[string]*protobufs.AgentConfigFile{
			"": {Body: []byte(configStr)},
		},
	}

	notifyNextStatusUpdate := make(chan struct{}, 1)
	data.AllAgents.SetCustomConfigForAgent(instanceId, config, notifyNextStatusUpdate)

	// Wait for up to 5 seconds for a Status update, which is expected
	// to be reported by the Agent after we set the remote config.
	timer := time.NewTicker(time.Second * 5)

	select {
	case <-notifyNextStatusUpdate:
	case <-timer.C:
	}

	http.Redirect(w, r, "/agent?instanceid="+uid.String(), http.StatusSeeOther)
}

func saveCustomConfigForInstanceJSON(w http.ResponseWriter, r *http.Request) {
	// Set Content-Type header for JSON response
	w.Header().Set("Content-Type", "application/json")

	if err := r.ParseForm(); err != nil {
		response := ConfigUpdateResponse{
			Success: false,
			Message: "Error parsing form: " + err.Error(),
		}
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(response)
		return
	}

	uid, err := uuid.Parse(r.Form.Get("instanceid"))
	if err != nil {
		response := ConfigUpdateResponse{
			Success: false,
			Message: "Invalid instance ID: " + err.Error(),
		}
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(response)
		return
	}

	instanceId := data.InstanceId(uid)
	agent := data.AllAgents.GetAgentReadonlyClone(instanceId)
	if agent == nil {
		response := ConfigUpdateResponse{
			Success:    false,
			Message:    "Agent not found",
			InstanceId: uid.String(),
		}
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(response)
		return
	}

	configStr := r.PostForm.Get("config")
	if configStr == "" {
		response := ConfigUpdateResponse{
			Success:    false,
			Message:    "Configuration cannot be empty",
			InstanceId: uid.String(),
		}
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(response)
		return
	}

	config := &protobufs.AgentConfigMap{
		ConfigMap: map[string]*protobufs.AgentConfigFile{
			"": {Body: []byte(configStr)},
		},
	}

	notifyNextStatusUpdate := make(chan struct{}, 1)
	data.AllAgents.SetCustomConfigForAgent(instanceId, config, notifyNextStatusUpdate)

	// Wait for up to 5 seconds for a Status update, which is expected
	// to be reported by the Agent after we set the remote config.
	timer := time.NewTicker(time.Second * 5)

	var statusReady bool
	select {
	case <-notifyNextStatusUpdate:
		statusReady = true
	case <-timer.C:
		statusReady = false
	}

	response := ConfigUpdateResponse{
		Success:     true,
		Message:     "Custom configuration updated successfully",
		InstanceId:  uid.String(),
		StatusReady: statusReady,
	}

	if !statusReady {
		response.Message += " (timeout waiting for agent confirmation)"
	}

	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Printf("Error encoding JSON response: %v", err)
	}
}

func rotateInstanceClientCert(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Find the agent instance.
	uid, err := uuid.Parse(r.Form.Get("instanceid"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	instanceId := data.InstanceId(uid)
	agent := data.AllAgents.GetAgentReadonlyClone(instanceId)
	if agent == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Create a new certificate for the agent.
	certificate, err := internal.CreateTLSCert("../../certs/certs/ca.cert.pem", "../../certs/private/ca.key.pem")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logger.Println(err)
		return
	}

	// Create an offer for the agent.
	offers := &protobufs.ConnectionSettingsOffers{
		Opamp: &protobufs.OpAMPConnectionSettings{
			Certificate: certificate,
		},
	}

	// Send the offer to the agent.
	data.AllAgents.OfferAgentConnectionSettings(instanceId, offers)

	logger.Printf("Waiting for agent %s to reconnect\n", instanceId)

	// Wait for up to 5 seconds for a Status update, which is expected
	// to be reported by the agent after we set the remote config.
	timer := time.NewTicker(time.Second * 5)

	// TODO: wait for agent to reconnect instead of waiting full 5 seconds.

	select {
	case <-timer.C:
		logger.Printf("Time out waiting for agent %s to reconnect\n", instanceId)
	}

	http.Redirect(w, r, "/agent?instanceid="+uid.String(), http.StatusSeeOther)
}

func opampConnectionSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Find the agent instance.
	uid, err := uuid.Parse(r.Form.Get("instanceid"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	instanceId := data.InstanceId(uid)
	agent := data.AllAgents.GetAgentReadonlyClone(instanceId)
	if agent == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// parse tls_min
	tlsMinVal := r.Form.Get("tls_min")
	var tlsMin string
	switch tlsMinVal {
	case "TLSv1.0":
		tlsMin = "1.0"
	case "TLSv1.1":
		tlsMin = "1.1"
	case "TLSv1.2":
		tlsMin = "1.2"
	case "TLSv1.3":
		tlsMin = "1.3"
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	offers := &protobufs.ConnectionSettingsOffers{
		Opamp: &protobufs.OpAMPConnectionSettings{
			Tls: &protobufs.TLSConnectionSettings{
				CaPemContents: opampCA(),
				MinVersion:    tlsMin,
				MaxVersion:    "1.3",
			},
		},
	}

	rawProxyURL := r.Form.Get("proxy_url")
	if len(rawProxyURL) > 0 {
		proxyURL, err := url.Parse(rawProxyURL)
		if err != nil {
			logger.Printf("Unable to parse %q as URL: %v", rawProxyURL, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		offers.Opamp.Proxy = &protobufs.ProxyConnectionSettings{
			Url: proxyURL.String(),
		}
	}

	data.AllAgents.OfferAgentConnectionSettings(instanceId, offers)

	logger.Printf("Waiting for agent %s to reconnect\n", instanceId)

	// Wait for up to 5 seconds for a Status update, which is expected
	// to be reported by the agent after we set the remote config.
	timer := time.NewTicker(time.Second * 5)

	// TODO: wait for agent to reconnect instead of waiting full 5 seconds.

	select {
	case <-timer.C:
		logger.Printf("Time out waiting for agent %s to reconnect\n", instanceId)
	}

	http.Redirect(w, r, "/agent?instanceid="+uid.String(), http.StatusSeeOther)
}
