package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

const appOptionsScript = `<script>(function(){
function optionNode(value){var option=document.createElement('option');option.value=value.value;option.textContent=value.description?value.text+' — '+value.description:value.text;return option}
async function load(control){
 var input=control.querySelector('[data-options-query]'),button=control.querySelector('[data-options-load]'),results=control.querySelector('[data-options-results]'),choose=control.querySelector('[data-options-choose]'),status=control.querySelector('[data-options-status]'),form=control.closest('form');
 if(!input||!button||!results||!status||!form)return;
 var minimum=parseInt(control.getAttribute('data-min-query')||'3',10),value=input.value.trim();
 if(value.length<minimum){status.textContent='Type at least '+minimum+' characters.';input.focus();return}
 var csrf=form.querySelector('input[name="_csrf"]'),params=new URLSearchParams();
 params.set('_csrf',csrf?csrf.value:'');params.set('app_id',control.getAttribute('data-app-id')||'');params.set('message_id',control.getAttribute('data-message-id')||'');params.set('view_id',control.getAttribute('data-view-id')||'');params.set('block_id',control.getAttribute('data-block-id')||'');params.set('action_id',control.getAttribute('data-action-id')||'');params.set('channel',control.getAttribute('data-channel')||'');params.set('query',value);
 button.disabled=true;results.disabled=true;if(choose)choose.disabled=true;status.textContent='Loading options…';
 try{
  var response=await fetch('/app/options',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/x-www-form-urlencoded','Accept':'application/json'},body:params.toString()});
  var body=await response.json();if(!response.ok)throw new Error(body.error||'Options could not be loaded.');
  results.replaceChildren();var groups=new Map();
  (body.options||[]).forEach(function(value){if(value.group){var group=groups.get(value.group);if(!group){group=document.createElement('optgroup');group.label=value.group;groups.set(value.group,group);results.appendChild(group)}group.appendChild(optionNode(value))}else{results.appendChild(optionNode(value))}});
  var count=(body.options||[]).length;results.disabled=count===0;if(choose)choose.disabled=count===0;status.textContent=count?count+' option'+(count===1?'':'s')+' loaded.':'No matching options.';
 }catch(error){status.textContent=error&&error.message?error.message:'Options could not be loaded.'}
 finally{button.disabled=false}
}
document.addEventListener('click',function(event){var button=event.target.closest('[data-options-load]');if(button){event.preventDefault();load(button.closest('[data-app-options]'))}});
document.addEventListener('keydown',function(event){if(event.key==='Enter'&&event.target.matches('[data-options-query]')){event.preventDefault();load(event.target.closest('[data-app-options]'))}});
})();</script>`

func (h Handler) appOptions(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w, workspaceContentSecurityPolicy)
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeOptionsError(w, http.StatusUnauthorized, "Sign in again to load app options.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil || len(r.Form["_csrf"]) != 1 {
		h.writeOptionsError(w, http.StatusBadRequest, "The option request could not be read.")
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	query := domain.AppOptionQuery{
		AppID: domain.AppID(strings.TrimSpace(r.Form.Get("app_id"))), MessageID: domain.MessageID(strings.TrimSpace(r.Form.Get("message_id"))),
		ViewID: domain.ViewID(strings.TrimSpace(r.Form.Get("view_id"))), BlockID: strings.TrimSpace(r.Form.Get("block_id")),
		ActionID: strings.TrimSpace(r.Form.Get("action_id")), Value: r.Form.Get("query"),
	}
	options, err := h.Messages.LoadAppOptions(
		r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), query, h.responseBaseURL(r),
	)
	if err != nil {
		status := http.StatusBadGateway
		message := "The app could not load options. Try again."
		switch {
		case errors.Is(err, store.ErrNotFound):
			status, message = http.StatusNotFound, "That dynamic menu is no longer available."
		case errors.Is(err, service.ErrInvalidAppResponse):
			message = "The app returned invalid options."
		case errors.Is(err, service.ErrAppInteractionUnavailable):
			message = "The app did not provide options in time."
		}
		h.writeOptionsError(w, status, message)
		return
	}
	type option struct {
		Text        string `json:"text"`
		Value       string `json:"value"`
		Description string `json:"description,omitempty"`
		Group       string `json:"group,omitempty"`
	}
	response := struct {
		Options []option `json:"options"`
	}{Options: make([]option, 0, len(options))}
	for _, value := range options {
		response.Options = append(response.Options, option{
			Text: value.Text, Value: value.Value, Description: value.Description, Group: value.Group,
		})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

func (h Handler) writeOptionsError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
