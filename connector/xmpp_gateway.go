package connector

import (
	"context"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
	"time"

	"gosrc.io/xmpp/stanza"
)

const (
	NSCommands = "http://jabber.org/protocol/commands"
	NSGateway  = "jabber:iq:gateway"
)

type AdHocCommand struct {
	XMLName   xml.Name            `xml:"http://jabber.org/protocol/commands command"`
	Node      string              `xml:"node,attr,omitempty"`
	Action    string              `xml:"action,attr,omitempty"`
	Status    string              `xml:"status,attr,omitempty"`
	SessionID string              `xml:"sessionid,attr,omitempty"`
	Actions   *AdHocCommandAction `xml:"actions,omitempty"`
	Form      *XData              `xml:"jabber:x:data x,omitempty"`
	Notes     []AdHocCommandNote  `xml:"note,omitempty"`
}

func (c *AdHocCommand) Namespace() string {
	return c.XMLName.Space
}

func (c *AdHocCommand) GetSet() *stanza.ResultSet {
	return nil
}

type AdHocCommandAction struct {
	Execute  string        `xml:"execute,attr,omitempty"`
	Next     *XDataBoolean `xml:"next,omitempty"`
	Prev     *XDataBoolean `xml:"prev,omitempty"`
	Complete *XDataBoolean `xml:"complete,omitempty"`
}

type AdHocCommandNote struct {
	Type string `xml:"type,attr,omitempty"`
	Text string `xml:",chardata"`
}

type GatewayQuery struct {
	XMLName xml.Name `xml:"jabber:iq:gateway query"`
	Desc    string   `xml:"desc,omitempty"`
	Prompt  string   `xml:"prompt,omitempty"`
	JID     string   `xml:"jid,omitempty"`
}

func (q *GatewayQuery) Namespace() string {
	return q.XMLName.Space
}

func (q *GatewayQuery) GetSet() *stanza.ResultSet {
	return nil
}

type GatewayActionDiscovery struct {
	TargetJID          string
	SupportsCommands   bool
	SupportsGatewayIQ  bool
	GatewayDescription string
	GatewayPrompt      string
	GatewayPromptError string
	CommandListError   string
	Commands           []stanza.DiscoItem
}

func init() {
	stanza.TypeRegistry.MapExtension(stanza.PKTIQ, xml.Name{Space: NSCommands, Local: "command"}, AdHocCommand{})
	stanza.TypeRegistry.MapExtension(stanza.PKTIQ, xml.Name{Space: NSGateway, Local: "query"}, GatewayQuery{})
}

func (nc *MyNetworkClient) requestDiscoItemsNode(ctx context.Context, jid, node string) (*stanza.DiscoItems, error) {
	if nc.xmppClient == nil {
		return nil, fmt.Errorf("XMPP client not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	itemsIQ, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		To:   jid,
		Id:   fmt.Sprintf("disco_items_node_%d", time.Now().UnixNano()),
	})
	itemsIQ.Payload = &stanza.DiscoItems{Node: node}
	ch, err := nc.xmppClient.SendIQ(ctx, itemsIQ)
	if err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			detail := formatIQError(resp.Error)
			if detail != "" {
				return nil, fmt.Errorf("disco items failed: %s (%s)", resp.Type, detail)
			}
			return nil, fmt.Errorf("disco items failed: %s", resp.Type)
		}
		payload, ok := resp.Payload.(*stanza.DiscoItems)
		if !ok || payload == nil {
			return nil, fmt.Errorf("unexpected disco items payload")
		}
		return payload, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (nc *MyNetworkClient) requestGatewayPrompt(ctx context.Context, jid string) (*GatewayQuery, error) {
	if nc.xmppClient == nil {
		return nil, fmt.Errorf("XMPP client not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	iq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		To:   jid,
		Id:   fmt.Sprintf("gateway_prompt_%d", time.Now().UnixNano()),
	})
	iq.Payload = &GatewayQuery{}
	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			detail := formatIQError(resp.Error)
			if detail != "" {
				return nil, fmt.Errorf("gateway prompt failed: %s (%s)", resp.Type, detail)
			}
			return nil, fmt.Errorf("gateway prompt failed: %s", resp.Type)
		}
		payload, ok := resp.Payload.(*GatewayQuery)
		if !ok || payload == nil {
			return nil, fmt.Errorf("unexpected gateway prompt payload")
		}
		return payload, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (nc *MyNetworkClient) discoverGatewayActions(ctx context.Context, targetJID string) (*GatewayActionDiscovery, error) {
	info, err := nc.requestDiscoInfo(ctx, targetJID)
	if err != nil {
		return nil, err
	}
	result := &GatewayActionDiscovery{TargetJID: targetJID}
	for _, identity := range info.Identity {
		if result.GatewayDescription == "" && identity.Name != "" {
			result.GatewayDescription = identity.Name
		}
	}
	result.SupportsCommands = hasDiscoFeature(info, NSCommands)
	result.SupportsGatewayIQ = hasDiscoFeature(info, NSGateway)
	if result.SupportsGatewayIQ {
		prompt, err := nc.requestGatewayPrompt(ctx, targetJID)
		if err != nil {
			result.GatewayPromptError = err.Error()
		} else if prompt != nil {
			result.GatewayDescription = firstNonEmpty(prompt.Desc, result.GatewayDescription)
			result.GatewayPrompt = strings.TrimSpace(prompt.Prompt)
		}
	}
	if result.SupportsCommands {
		items, err := nc.requestDiscoItemsNode(ctx, targetJID, NSCommands)
		if err != nil {
			result.CommandListError = err.Error()
		} else {
			result.Commands = append(result.Commands, items.Items...)
			sort.Slice(result.Commands, func(i, j int) bool {
				left := firstNonEmpty(result.Commands[i].Name, result.Commands[i].Node, result.Commands[i].JID)
				right := firstNonEmpty(result.Commands[j].Name, result.Commands[j].Node, result.Commands[j].JID)
				return strings.ToLower(left) < strings.ToLower(right)
			})
		}
	}
	return result, nil
}

func (nc *MyNetworkClient) executeGatewayCommand(ctx context.Context, targetJID, node string) (*AdHocCommand, error) {
	if nc.xmppClient == nil {
		return nil, fmt.Errorf("XMPP client not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	iq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeSet,
		To:   targetJID,
		Id:   fmt.Sprintf("gateway_command_%d", time.Now().UnixNano()),
	})
	iq.Payload = &AdHocCommand{Node: node, Action: "execute"}
	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return nil, err
	}
	return waitForGatewayCommandResponse(ctx, ch)
}

func (nc *MyNetworkClient) submitGatewayCommand(ctx context.Context, pending PendingGatewayForm, values map[string]string) (*AdHocCommand, error) {
	if nc.xmppClient == nil {
		return nil, fmt.Errorf("XMPP client not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	iq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeSet,
		To:   pending.TargetJID,
		Id:   fmt.Sprintf("gateway_submit_%d", time.Now().UnixNano()),
	})
	iq.Payload = &AdHocCommand{
		Node:      pending.Node,
		Action:    firstNonEmpty(pending.DefaultAction, "execute"),
		SessionID: pending.RemoteSessionID,
		Form:      buildGatewaySubmitForm(pending, values),
	}
	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return nil, err
	}
	return waitForGatewayCommandResponse(ctx, ch)
}

func waitForGatewayCommandResponse(ctx context.Context, ch <-chan stanza.IQ) (*AdHocCommand, error) {
	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			detail := formatIQError(resp.Error)
			if detail != "" {
				return nil, fmt.Errorf("command failed: %s (%s)", resp.Type, detail)
			}
			return nil, fmt.Errorf("command failed: %s", resp.Type)
		}
		payload, ok := resp.Payload.(*AdHocCommand)
		if !ok || payload == nil {
			return nil, fmt.Errorf("unexpected command payload")
		}
		return payload, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func buildGatewaySubmitForm(pending PendingGatewayForm, values map[string]string) *XData {
	fields := make([]XDataField, 0, len(pending.Fields))
	for _, field := range pending.Fields {
		fieldValues := append([]string(nil), field.Values...)
		if value, ok := values[field.Var]; ok {
			fieldValues = []string{normalizeGatewaySubmissionValue(field.Type, value)}
		}
		if field.Type != "hidden" && len(fieldValues) == 0 {
			continue
		}
		fields = append(fields, XDataField{
			Var:    field.Var,
			Type:   field.Type,
			Values: fieldValues,
		})
	}
	return &XData{Type: "submit", Fields: fields}
}

func normalizeGatewaySubmissionValue(fieldType, value string) string {
	value = strings.TrimSpace(value)
	if fieldType != "boolean" {
		return value
	}
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return "1"
	case "0", "false", "no", "off":
		return "0"
	default:
		return value
	}
}

func gatewayPendingFormFromCommand(targetJID string, cmd *AdHocCommand) PendingGatewayForm {
	form := PendingGatewayForm{
		TargetJID:       targetJID,
		Node:            cmd.Node,
		RemoteSessionID: cmd.SessionID,
		CreatedAt:       time.Now().UTC(),
		LastStatus:      cmd.Status,
		DefaultAction:   "execute",
	}
	if cmd.Actions != nil {
		form.DefaultAction = firstNonEmpty(cmd.Actions.Execute, form.DefaultAction)
		form.AllowedActions = gatewayAllowedActions(cmd.Actions)
	}
	for _, note := range cmd.Notes {
		text := strings.TrimSpace(note.Text)
		if text != "" {
			form.Notes = append(form.Notes, text)
		}
	}
	if cmd.Form == nil {
		return form
	}
	form.Title = strings.TrimSpace(cmd.Form.Title)
	for _, instruction := range cmd.Form.Instructions {
		instruction = strings.TrimSpace(instruction)
		if instruction != "" {
			form.Instructions = append(form.Instructions, instruction)
		}
	}
	for _, field := range cmd.Form.Fields {
		if strings.TrimSpace(field.Var) == "http://jabber.org/protocol/commands#actions" {
			continue
		}
		pendingField := PendingGatewayField{
			Var:         strings.TrimSpace(field.Var),
			Type:        strings.TrimSpace(field.Type),
			Label:       strings.TrimSpace(field.Label),
			Description: strings.TrimSpace(field.Description),
			Required:    field.Required != nil,
			Values:      append([]string(nil), field.Values...),
		}
		for _, option := range field.Options {
			pendingField.Options = append(pendingField.Options, PendingGatewayOption{
				Label: strings.TrimSpace(option.Label),
				Value: strings.TrimSpace(option.Value),
			})
		}
		form.Fields = append(form.Fields, pendingField)
	}
	return form
}

func gatewayAllowedActions(actions *AdHocCommandAction) []string {
	if actions == nil {
		return nil
	}
	allowed := make([]string, 0, 3)
	if actions.Prev != nil {
		allowed = append(allowed, "prev")
	}
	if actions.Next != nil {
		allowed = append(allowed, "next")
	}
	if actions.Complete != nil {
		allowed = append(allowed, "complete")
	}
	if actions.Execute != "" {
		already := false
		for _, action := range allowed {
			if action == actions.Execute {
				already = true
				break
			}
		}
		if !already {
			allowed = append(allowed, actions.Execute)
		}
	}
	return allowed
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (nc *MyNetworkClient) storePendingGatewayForm(ctx context.Context, pending PendingGatewayForm) (string, error) {
	meta := nc.ensureLoginMetadata()
	if meta.PendingGatewayForms == nil {
		meta.PendingGatewayForms = make(map[string]PendingGatewayForm)
	}
	nc.pruneExpiredGatewayForms(meta)
	key := fmt.Sprintf("gw-%d", time.Now().UnixNano())
	meta.PendingGatewayForms[key] = pending
	if err := nc.login.Save(ctx); err != nil {
		return "", err
	}
	return key, nil
}

func (nc *MyNetworkClient) replacePendingGatewayForm(ctx context.Context, key string, pending PendingGatewayForm) error {
	meta := nc.ensureLoginMetadata()
	if meta.PendingGatewayForms == nil {
		meta.PendingGatewayForms = make(map[string]PendingGatewayForm)
	}
	nc.pruneExpiredGatewayForms(meta)
	meta.PendingGatewayForms[key] = pending
	return nc.login.Save(ctx)
}

func (nc *MyNetworkClient) getPendingGatewayForm(key string) (PendingGatewayForm, bool) {
	meta, _ := nc.login.Metadata.(*LoginMetadata)
	if meta == nil || meta.PendingGatewayForms == nil {
		return PendingGatewayForm{}, false
	}
	nc.pruneExpiredGatewayForms(meta)
	pending, ok := meta.PendingGatewayForms[key]
	return pending, ok
}

func (nc *MyNetworkClient) deletePendingGatewayForm(ctx context.Context, key string) error {
	meta, _ := nc.login.Metadata.(*LoginMetadata)
	if meta == nil || meta.PendingGatewayForms == nil {
		return nil
	}
	delete(meta.PendingGatewayForms, key)
	return nc.login.Save(ctx)
}

func (nc *MyNetworkClient) ensureLoginMetadata() *LoginMetadata {
	meta, _ := nc.login.Metadata.(*LoginMetadata)
	if meta == nil {
		meta = &LoginMetadata{}
		nc.login.Metadata = meta
	}
	return meta
}

func (nc *MyNetworkClient) pruneExpiredGatewayForms(meta *LoginMetadata) {
	if meta == nil || meta.PendingGatewayForms == nil {
		return
	}
	cutoff := time.Now().Add(-30 * time.Minute)
	for key, pending := range meta.PendingGatewayForms {
		if pending.CreatedAt.IsZero() || pending.CreatedAt.After(cutoff) {
			continue
		}
		delete(meta.PendingGatewayForms, key)
	}
}
