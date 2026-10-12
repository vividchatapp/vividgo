package main

import "net/http"

const setupCSS = `body{font-family:system-ui,sans-serif;background:#0f1115;color:#e8eaf0;margin:0}
.wrap{max-width:860px;margin:0 auto;padding:24px}
.card{background:#171a21;border:1px solid #2a2f3a;border-radius:12px;padding:20px;margin:16px 0}
label{display:block;font-size:13px;color:#9aa3b2;margin:10px 0 4px}
input,select{width:100%;padding:10px;border-radius:8px;border:1px solid #2a2f3a;background:#0f141b;color:#fff;box-sizing:border-box}
button{background:#4f7cff;border:0;color:#fff;padding:10px 16px;border-radius:8px;cursor:pointer;margin:6px 6px 0 0}
button.ghost{background:#2a2f3a}
.row{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.ok{color:#7ddf9a}.err{color:#ff8a8a}.muted{color:#9aa3b2;font-size:13px}
.bot,.prov{border:1px solid #2a2f3a;border-radius:8px;padding:12px;margin:10px 0}
a{color:#8fb4ff}`

func handleSetupPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte("<!doctype html><html><head><meta charset=\"utf-8\">" +
		"<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">" +
		"<title>VividGo Setup</title><style>" + setupCSS + "</style></head><body><div class=\"wrap\">" +
		"<h1>VividGo Setup</h1>" +
		"<p class=\"muted\">Backend does the work -- this page edits config.yaml. <a href=\"/chat\">Open chat</a></p>" +
		"<div class=\"card\"><h2>1. Telegram</h2>" +
		"<label>Telegram User ID (from @userinfobot)</label><input id=\"user_id\" type=\"number\">" +
		"<div id=\"bots\"></div><button class=\"ghost\" onclick=\"addBot()\">+ Add bot</button></div>" +
		"<div class=\"card\"><h2>2. Providers (Ollama + Google)</h2><div id=\"provs\"></div>" +
		"<button class=\"ghost\" onclick=\"addProv('ollama')\">+ Ollama</button> " +
		"<button class=\"ghost\" onclick=\"addProv('google')\">+ Google</button></div>" +
		"<div class=\"card\"><h2>3. Save</h2>" +
		"<label><input id=\"debug\" type=\"checkbox\" style=\"width:auto\"> Debug</label><br>" +
		"<button onclick=\"save()\">Save config.yaml</button> <span id=\"saveMsg\" class=\"muted\"></span></div>" +
		"</div><script src=\"/setup.js\"></script></body></html>"))
}


const setupJSa = `async function api(p,o){const r=await fetch(p,o);if(!r.ok)throw new Error(await r.text());return r.json();}
function addBot(){document.getElementById('bots').appendChild(botRow());}
function addProv(t){document.getElementById('provs').appendChild(provRow({type:t}));}
function provData(row){return{type:row.querySelector('.ptype').value,api_key:row.querySelector('.pkey').value,model:row.querySelector('.pmodel').value,api_base:row.querySelector('.pbase').value};}
async function save(){const bots=Array.from(document.querySelectorAll('#bots .bot')).map(function(r){return{name:r.querySelector('.bname').value,token:r.querySelector('.btok').value};});
const provs=Array.from(document.querySelectorAll('#provs .prov')).map(function(r){const d=provData(r);return{name:r.querySelector('.pname').value,type:d.type,api_key:d.api_key,model:d.model,api_base:d.api_base};});
const m=document.getElementById('saveMsg');m.textContent='saving...';
try{await api('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({user_id:Number(document.getElementById('user_id').value||0),debug:document.getElementById('debug').checked,bots:bots,providers:provs})});m.textContent='saved. open /chat';}catch(e){m.textContent='ERR '+e.message;}}
(async function(){try{const j=await api('/api/config');document.getElementById('user_id').value=j.user_id||'';
document.getElementById('debug').checked=!!j.debug;
(j.bots||[]).forEach(function(b){document.getElementById('bots').appendChild(botRow(b));});
(j.providers||[]).forEach(function(p){document.getElementById('provs').appendChild(provRow(p));});
if(!(j.bots||[]).length)addBot();if(!(j.providers||[]).length)addProv('ollama');}catch(e){addBot();addProv('ollama');}})();`

const setupJSb = `function botRow(b){b=b||{};const d=document.createElement('div');d.className='bot';
d.innerHTML='<div class=row><div><label>Bot name</label><input class=bname></div><div><label>Token</label><input class=btok type=password></div></div><button class=ghost onclick="testTg(this)">Test</button> <span class=msgmuted></span>';d.querySelector('.bname').value=b.name||'';return d;}
function provRow(p){p=p||{type:'ollama'};const d=document.createElement('div');d.className='prov';
d.innerHTML='<div class=row><div><label>Name</label><input class=pname></div><div><label>Type</label><select class=ptype><option value=ollama>Ollama</option><option value=google>Google</option></select></div></div><div class=row><div><label>API key</label><input class=pkey type=password></div><div><label>API base</label><input class=pbase></div></div><div class=row><div><label>Model</label><input class=pmodel></div><div><label>Fetched</label><select class=pmodellist><option value="">--</option></select></div></div><button class=ghost onclick="loadModels(this)">Models</button> <button class=ghost onclick="testProv(this)">Test</button> <span class=msg></span>';
d.querySelector('.pname').value=p.name||'';d.querySelector('.ptype').value=p.type||'ollama';
d.querySelector('.pkey').value=p.api_key||'';d.querySelector('.pbase').value=p.base||'';d.querySelector('.pmodel').value=p.model||'';
d.querySelector('.pmodellist').onchange=function(){d.querySelector('.pmodel').value=this.value;};return d;}
async function testTg(btn){const row=btn.parentElement;const m=row.querySelector('.msgmuted');m.textContent='...';
try{const j=await api('/api/test/telegram',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token:row.querySelector('.btok').value})});m.textContent='OK @'+j.username;}catch(e){m.textContent='ERR '+e.message;}}
async function testProv(btn){const row=btn.closest('.prov');const m=row.querySelector('.msg');m.textContent='...';
try{const j=await api('/api/test/provider',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(provData(row))});m.textContent='OK';fillModels(row,j.models||[]);}catch(e){m.textContent='ERR '+e.message;}}
function fillModels(row,models){const s=row.querySelector('.pmodellist');s.innerHTML='';models.forEach(function(x){const o=document.createElement('option');o.value=x;o.textContent=x;s.appendChild(o);});}
async function loadModels(btn){const row=btn.closest('.prov');const d=provData(row);
try{const j=await api('/api/models?'+new URLSearchParams({type:d.type,key:d.api_key,model:d.model,base:d.api_base}));fillModels(row,j.models||[]);}catch(e){alert(e.message);}};`

func handleSetupJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Write([]byte(setupJSa + setupJSb))
}

const chatPageA = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>VividGo Chat</title><style>
body{font-family:system-ui,sans-serif;background:#0f1115;color:#e8eaf0;margin:0;height:100vh;overflow:hidden}
.wrap{display:grid;grid-template-columns:220px 1fr;grid-template-rows:100%;height:100vh;overflow:hidden}
.side{border-right:1px solid #2a2f3a;padding:12px;overflow-y:auto;min-height:0;overflow-x:hidden}
.main{display:flex;flex-direction:column;min-height:0;overflow:hidden}
#msgs{flex:1;overflow:auto;padding:16px;display:flex;flex-direction:column;gap:10px;min-height:0}
.b{padding:8px 26px 8px 12px;border-radius:10px;max-width:75%;white-space:pre-wrap;position:relative}
.copy{position:absolute;top:4px;right:4px;opacity:0;padding:1px 5px;font-size:12px;line-height:1.5;background:#0f141b;border:1px solid #2a2f3a;color:#9aa3b2;border-radius:5px;cursor:pointer;transition:opacity .12s}
.b:hover .copy{opacity:1}.copy:focus{opacity:1}.copy.ok{color:#5ac47a;opacity:1}
.u{background:#2a4bff;align-self:flex-end}.a{background:#1b1f29;align-self:flex-start}
.bar{padding:12px;border-top:1px solid #2a2f3a;display:flex;gap:8px}
#in{flex:1;padding:10px;border-radius:8px;border:1px solid #2a2f3a;background:#0f141b;color:#fff;font:inherit;line-height:1.45;resize:none;min-height:44px;max-height:180px;overflow-y:auto}
button{background:#4f7cff;border:0;color:#fff;padding:10px 16px;border-radius:8px;cursor:pointer}
.botbtn{display:block;width:100%;text-align:left;margin:4px 0;background:#1b1f29}
.botbtn.on{background:#4f7cff}.muted{color:#9aa3b2;font-size:12px}a{color:#8fb4ff}
#msgs pre{background:#0f141b;border:1px solid #2a2f3a;border-radius:8px;padding:8px;overflow:auto;margin:4px 0}
#msgs code{background:#0f141b;padding:1px 5px;border-radius:4px;font-family:ui-monospace,monospace}
#msgs pre code{padding:0;background:none}
.a.cmd{background:#141a12;border-left:3px solid #4f7cff;white-space:pre-wrap;font-family:ui-monospace,monospace;font-size:12px}
</style></head><body><div class="wrap"><div class="side"><h3>Bots</h3><div id="bots"></div><p class="muted"><a href="/setup">Setup</a><br><span id="conn">...</span></p><p class="muted">Commands start with a dot, e.g. <code>.help</code>, <code>.model 3</code>, <code>.status</code>.</p></div><div class="main"><div id="msgs"></div><div class="bar"><textarea id="in" rows="1" placeholder="Message or .command... (Enter to send, Ctrl+Enter for a new line)"></textarea><button onclick="send()">Send</button></div></div></div><script>`

const chatPageB = `let cur=null;
async function api(p,o){const r=await fetch(p,o);if(!r.ok)throw new Error(await r.text());return r.json();}
function esc(s){return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');}
function inlineMd(t){t=esc(t);t=t.replace(/\x60([^\x60\n]+)\x60/g,'<code>$1</code>');t=t.replace(/\*\*([^*]+)\*\*/g,'<strong>$1</strong>');t=t.replace(/\*([^*\n]+)\*/g,'<em>$1</em>');t=t.replace(/(^|[^A-Za-z0-9])_([^_\n]+)_(?![A-Za-z0-9])/g,'$1<em>$2</em>');return t;}
function md(t){var p=String(t).split(/\x60\x60\x60/);for(var i=0;i<p.length;i++){if(i%2){var l=p[i].split('\n');if(l.length>1&&/^[A-Za-z0-9+#._-]+$/.test(l[0])){l.shift();}p[i]='<pre><code>'+esc(l.join('\n'))+'</code></pre>';}else{p[i]=inlineMd(p[i]);}}return p.join('');}
// copyText writes raw message text to the clipboard. The Clipboard API needs a
// secure context (localhost counts), so fall back to a hidden textarea when it
// is unavailable, and flash a tick on the button as confirmation.
async function copyText(t,btn){let ok=false;
try{await navigator.clipboard.writeText(t);ok=true;}catch(_){
const ta=document.createElement('textarea');ta.value=t;ta.style.position='fixed';ta.style.opacity='0';
document.body.appendChild(ta);ta.select();
try{ok=document.execCommand('copy');}catch(__){ok=false;}
document.body.removeChild(ta);}
const prev=btn.textContent;btn.textContent=ok?'\u2713':'!';btn.classList.add('ok');
setTimeout(function(){btn.textContent=prev;btn.classList.remove('ok');},1100);}
function add(role,text,cmd){const d=document.createElement('div');d.className='b '+(role==='user'?'u':'a')+(cmd?' cmd':'');if(role==='user'){d.textContent=text;}else{d.innerHTML=md(text);}
// Copy the raw text, not the rendered markup. The text argument is captured per
// bubble so each button copies its own message.
const cp=document.createElement('button');cp.type='button';cp.className='copy';cp.title='Copy message';cp.textContent='\u29c9';
cp.addEventListener('click',function(e){e.stopPropagation();copyText(text,cp);});
d.appendChild(cp);
const m=document.getElementById('msgs');m.appendChild(d);m.scrollTop=1e9;}
// playAudio plays a base64 data URL (spoken voice replies). Browsers block
// autoplay until the user has interacted with the page; since sending a message
// is itself an interaction, playback started from send()/SSE is allowed. Any
// rejection is swallowed so a blocked play never breaks the chat UI.
function playAudio(url){try{const a=new Audio(url);const p=a.play();if(p&&p.catch)p.catch(function(){});}catch(_){}}
async function pick(name){cur=name;const bs=document.querySelectorAll('.botbtn');for(let i=0;i<bs.length;i++){bs[i].classList.toggle('on',bs[i].dataset.n===name);}document.getElementById('msgs').innerHTML='';
try{const j=await api('/api/history?bot='+encodeURIComponent(name));(j.messages||[]).forEach(function(x){add(x.role,x.content);});}catch(e){add('assistant','history err');}}
async function send(){const i=document.getElementById('in');const t=i.value.trim();if(!t||!cur)return;i.value='';autogrow(i);add('user',t);
try{const j=await api('/api/chat',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({bot:cur,message:t})});add('assistant',j.reply,!!j.command);if(j.audio)playAudio(j.audio);}catch(e){add('assistant','error: '+e.message);}}
// Enter sends; Ctrl+Enter (or Cmd+Enter) inserts a newline instead. In a
// textarea a bare Enter would otherwise just add a newline, so the send key
// has to be intercepted explicitly.
document.getElementById('in').addEventListener('keydown',function(e){
if(e.key!=='Enter'||e.shiftKey)return;
if(e.ctrlKey||e.metaKey)return;
e.preventDefault();
send();
});
// Grow the box with the content up to the CSS max-height, then scroll.
function autogrow(el){el.style.height='auto';el.style.height=Math.min(el.scrollHeight,180)+'px';}
document.getElementById('in').addEventListener('input',function(){autogrow(this);});
const es=new EventSource('/api/stream');es.onopen=function(){document.getElementById('conn').textContent='live';};
es.onmessage=function(e){try{const m=JSON.parse(e.data);if(m.bot!==cur||m.source!=='telegram')return;if(m.audio){playAudio(m.audio);}if(m.content){add('assistant',m.content);}}catch(_){}};
(async function(){try{const j=await api('/api/status');const bs=j.bots||[];
bs.forEach(function(n){const b=document.createElement('button');b.className='botbtn';b.dataset.n=n;b.textContent=n;b.onclick=function(){pick(n);};document.getElementById('bots').appendChild(b);});
if(bs.length&&!cur)pick(bs[0]);}catch(e){document.getElementById('bots').innerHTML='<p class="muted">failed to load bots</p>';}})();
</script></body></html>`

func handleChatPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(chatPageA + chatPageB))
}
