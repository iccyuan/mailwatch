"use strict";
const $=id=>document.getElementById(id);
let cfg=null, hOffset=0, hTotal=0, statusTimer=null, lastStatus=null;

function toast(msg,type){const t=document.createElement('div');t.className='toast '+(type||'');
  t.textContent=msg;$('toasts').appendChild(t);setTimeout(()=>t.remove(),4000)}
function esc(s){const d=document.createElement('span');d.textContent=s??'';return d.innerHTML}

async function api(path,opt){
  const r=await fetch(path,Object.assign({headers:{'Content-Type':'application/json'}},opt));
  if(r.status===401){showLogin();throw new Error('未登录')}
  const data=await r.json().catch(()=>({}));
  if(!r.ok)throw new Error(data.error||('HTTP '+r.status));
  return data;
}

/* ---------- 登录 ---------- */
function showLogin(){$('login').style.display='flex';$('app').style.display='none';
  if(statusTimer){clearInterval(statusTimer);statusTimer=null}}
async function showApp(){$('login').style.display='none';$('app').style.display='block';
  await loadConfig();refreshStatus();loadStats();
  if(!statusTimer)statusTimer=setInterval(refreshStatus,5000)}
let loggingIn=false;
async function doLogin(e){e.preventDefault();
  if(loggingIn)return; // 防双击/回车重复提交
  loggingIn=true;
  const btn=e.target.querySelector('button');btn.disabled=true;btn.textContent='登录中…';
  try{await api('/api/login',{method:'POST',body:JSON.stringify({username:$('lg-user').value,password:$('lg-pass').value})});
    toast('登录成功','ok');showApp()}catch(err){toast(err.message,'err')}
  loggingIn=false;btn.disabled=false;btn.textContent='登 录'}
async function doLogout(){try{await api('/api/logout',{method:'POST'})}catch(e){};showLogin()}
function openPwd(){$('m-pwd').classList.add('open')}
async function changePwd(){
  try{await api('/api/password',{method:'POST',body:JSON.stringify({old:$('p-old').value,new:$('p-new').value})});
    $('m-pwd').classList.remove('open');$('p-old').value=$('p-new').value='';toast('密码已修改','ok')}
  catch(err){toast(err.message,'err')}}

/* ---------- 导航 ---------- */
function go(el){document.querySelectorAll('.navi .item').forEach(t=>t.classList.remove('active'));
  document.querySelectorAll('.page').forEach(p=>p.classList.remove('active'));
  el.classList.add('active');$('page-'+el.dataset.page).classList.add('active');
  if(el.dataset.page==='history'){hOffset=0;fillMailboxSelects();loadHistory()}
  if(el.dataset.page==='overview')loadStats();
  if(el.dataset.page==='rules')fillMailboxSelects()}

/* ---------- 状态 ---------- */
async function refreshStatus(){try{
  const s=await api('/api/status');lastStatus=s;
  const total=(s.mailboxes||[]).length, on=(s.mailboxes||[]).filter(m=>m.connected).length;
  $('side-dot').className='dot '+(on===total&&total>0?'on':'off');
  $('side-conn').textContent=total?`${on}/${total} 个邮箱在线`:'未配置邮箱';
  $('mb-status').innerHTML=(s.mailboxes||[]).map(m=>
    `<span class="badge ${m.connected?'b-ok':'b-err'}"><span class="dot"></span>${esc(m.name)}
     <span style="color:var(--dim)">· ${m.connected?'监听中':'未连接'}${m.last_uid?' · UID '+m.last_uid:''}</span></span>`).join('')
    ||'<span class="hint">还没有配置收信邮箱</span>';
  $('events').innerHTML=(s.events||[]).map(e=>
    `<div class="ev ${e.level}"><span class="tm">${new Date(e.time).toLocaleString()}</span><span>${esc(e.msg)}</span></div>`).join('');
  $('about').textContent=`mailwatch v${s.version} · 运行 ${Math.floor(s.uptime_sec/3600)}h${Math.floor(s.uptime_sec%3600/60)}m · ${s.rules} 条规则`;
  updateMbDots();
}catch(e){}}
function updateMbDots(){if(!lastStatus)return;
  (lastStatus.mailboxes||[]).forEach(m=>{
    const el=document.querySelector(`[data-mbdot="${CSS.escape(m.name)}"]`);
    if(el)el.className='dot '+(m.connected?'on':'off')})}

/* ---------- 统计 ---------- */
async function loadStats(){try{
  const s=await api('/api/stats');
  $('stats').innerHTML=`
    <div class="stat"><div class="t">📅 今日</div><div class="duo">
      <div><span class="n blue">${s.today_received}</span><em>收到</em></div>
      <div><span class="n green">${s.today_forwarded}</span><em>转发</em></div></div></div>
    <div class="stat"><div class="t">📚 累计</div><div class="duo">
      <div><span class="n">${s.total}</span><em>收到</em></div>
      <div><span class="n blue">${s.matched}</span><em>命中</em></div></div></div>
    <div class="stat"><div class="t">📬 送达</div><div class="duo">
      <div><span class="n green">${s.delivered}</span><em>成功</em></div>
      <div><span class="n red">${s.failed}</span><em>失败</em></div></div></div>`;
  const max=Math.max(1,...s.days.map(d=>d.received));
  $('chart').innerHTML=s.days.map(d=>`<div class="col">
      <div class="bars">
        <div class="bar recv" style="height:${d.received/max*100}%" title="${d.date} 收到 ${d.received}"></div>
        <div class="bar fwd" style="height:${d.forwarded/max*100}%" title="${d.date} 转发 ${d.forwarded}"></div>
      </div><div class="d">${d.date}</div></div>`).join('');
  const parts=[];
  if(s.rules.length)parts.push(s.rules.map(r=>`<span class="badge" style="margin:0 8px 8px 0">🎯 ${esc(r.name)} · ${r.count} 次</span>`).join(''));
  if(s.mailboxes&&s.mailboxes.length)parts.push(s.mailboxes.map(m=>`<span class="badge" style="margin:0 8px 8px 0">📥 ${esc(m.name)} · ${m.count} 封</span>`).join(''));
  $('rule-stats').innerHTML=parts.join('<br>')||'还没有记录';
}catch(e){}}

/* ---------- 历史 ---------- */
function delivBadge(r){
  if(!r.rule)return '<span class="badge">未命中</span>';
  const ok=(r.results||[]).length&&r.results.every(x=>x.ok);
  return ok?'<span class="badge b-ok"><span class="dot"></span>已送达</span>'
           :'<span class="badge b-err"><span class="dot"></span>失败</span>'}
async function loadHistory(){try{
  const mb=$('h-mailbox').value||'';
  const d=await api(`/api/history?offset=${hOffset}&limit=50&q=${encodeURIComponent($('hq').value)}&mailbox=${encodeURIComponent(mb)}`);
  hTotal=d.total;
  $('h-total').textContent=`共 ${d.total} 封`;
  $('h-page').textContent=(hOffset/50+1)+' / '+Math.max(1,Math.ceil(d.total/50));
  $('h-body').innerHTML=d.items.length?d.items.map(r=>`<tr onclick="openMail('${r.id}')">
      <td>${new Date(r.time).toLocaleString()}</td>
      <td class="ellip" style="max-width:110px">${esc(r.mailbox||'—')}</td>
      <td class="ellip" style="max-width:170px">${esc(r.from||r.from_addr)}</td>
      <td class="ellip">${esc(r.subject)}</td>
      <td>${r.rule?'<span class="badge">'+esc(r.rule)+'</span>':'<span style="color:var(--dim)">—</span>'}</td>
      <td>${delivBadge(r)}</td></tr>`).join('')
    :'<tr><td colspan="6" style="text-align:center;color:var(--dim);padding:30px">暂无记录</td></tr>';
}catch(e){toast(e.message,'err')}}
function hPage(dir){const n=hOffset+dir*50;if(n<0||n>=hTotal)return;hOffset=n;loadHistory()}
async function openMail(id){try{
  const r=await api('/api/history/item?id='+id);
  $('md-subject').textContent=r.subject||'(无主题)';
  $('md-mailbox').textContent=r.mailbox||'—';
  $('md-from').textContent=(r.from||r.from_addr)+(r.from_addr&&r.from!==r.from_addr?' <'+r.from_addr+'>':'');
  $('md-time').textContent=new Date(r.time).toLocaleString();
  $('md-rule').textContent=r.rule||'未命中任何规则';
  $('md-deliv').innerHTML=(r.results||[]).map(x=>`<div class="deliv">
      ${x.ok?'<span class="b-ok">✅ 已送达</span>':'<span class="b-err">❌ 失败</span>'}
      → ${esc(x.target)} <span style="color:var(--dim)">(${x.action} · ${new Date(x.time).toLocaleString()})</span>
      ${x.error?'<div class="b-err" style="margin-top:4px">'+esc(x.error)+'</div>':''}</div>`).join('');
  $('md-body').textContent=r.body||'(无文本正文)';
  if(r.body_html){
    $('md-toggle').style.display='flex';
    $('md-html').srcdoc=r.body_html;
    mailView('html');
  }else{
    $('md-toggle').style.display='none';
    mailView('text');
  }
  $('m-mail').classList.add('open');
}catch(e){toast(e.message,'err')}}
function mailView(mode){
  const isHtml=mode==='html';
  $('md-html').style.display=isHtml?'block':'none';
  $('md-body').style.display=isHtml?'none':'block';
  $('md-btn-html').classList.toggle('on',isHtml);
  $('md-btn-text').classList.toggle('on',!isHtml);
}

/* ---------- chips ---------- */
function makeChips(container,values,validate){
  container.classList.add('chips');container.innerHTML='';
  const input=document.createElement('input');input.placeholder='输入后回车添加';
  const render=()=>{container.querySelectorAll('.chip').forEach(c=>c.remove());
    values.forEach((v,i)=>{const c=document.createElement('span');c.className='chip';
      c.innerHTML=esc(v)+' <b>×</b>';c.querySelector('b').onclick=()=>{values.splice(i,1);render()};
      container.insertBefore(c,input)})};
  const add=v=>{v=(v??input.value).trim();if(!v)return;
    if(validate&&!validate(v)){toast('「'+v+'」不是有效的邮箱地址','err');return}
    if(!values.includes(v))values.push(v);input.value='';render()};
  input.onkeydown=e=>{
    if(e.key==='Enter'||e.key===','){e.preventDefault();add()}
    else if(e.key==='Backspace'&&!input.value&&values.length){values.pop();render()}};
  input.onblur=()=>add();
  container.onclick=()=>input.focus();
  container.appendChild(input);render();
  return {add};
}
const isEmail=v=>/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v);

/* 密码框加显示/隐藏切换 */
function addEye(input){
  if(!input||input.dataset.eye)return;input.dataset.eye='1';
  const wrap=document.createElement('div');wrap.className='pwdwrap';
  input.parentNode.insertBefore(wrap,input);wrap.appendChild(input);
  const b=document.createElement('button');b.type='button';b.className='eye';b.textContent='👁';
  b.title='显示/隐藏';
  b.onclick=()=>{const show=input.type==='password';
    input.type=show?'text':'password';b.textContent=show?'🙈':'👁'};
  wrap.appendChild(b);
}

/* ---------- 服务商智能提示 ---------- */
const PROVIDERS={
  'qq.com':{name:'QQ 邮箱',imap:'imap.qq.com',smtp:'smtp.qq.com',ssl:true,
    tip:'需使用授权码:网页版邮箱 → 设置 → 账号 → 开启 IMAP/SMTP 服务 → 生成授权码'},
  'foxmail.com':{name:'QQ 邮箱(Foxmail)',imap:'imap.qq.com',smtp:'smtp.qq.com',ssl:true,tip:'需使用 QQ 邮箱授权码'},
  'vip.qq.com':{name:'QQ VIP 邮箱',imap:'imap.qq.com',smtp:'smtp.qq.com',ssl:true,tip:'需使用 QQ 邮箱授权码'},
  '163.com':{name:'网易 163',imap:'imap.163.com',smtp:'smtp.163.com',ssl:true,
    tip:'需使用授权码:网页版 → 设置 → POP3/SMTP/IMAP → 开启服务并获取授权码'},
  '126.com':{name:'网易 126',imap:'imap.126.com',smtp:'smtp.126.com',ssl:true,tip:'需使用授权码(同网易邮箱)'},
  'yeah.net':{name:'网易 yeah',imap:'imap.yeah.net',smtp:'smtp.yeah.net',ssl:true,tip:'需使用授权码(同网易邮箱)'},
  'gmail.com':{name:'Gmail',imap:'imap.gmail.com',smtp:'smtp.gmail.com',ssl:true,
    tip:'需使用应用专用密码(两步验证 → App passwords);服务器需能访问 Google'},
  'outlook.com':{name:'Outlook',imap:'outlook.office365.com',smtp:'smtp.office365.com',ssl:false,smtpPort:587,
    tip:'建议用应用密码;SMTP 走 587 STARTTLS'},
  'hotmail.com':{name:'Hotmail',imap:'outlook.office365.com',smtp:'smtp.office365.com',ssl:false,smtpPort:587,
    tip:'同 Outlook:587 STARTTLS + 应用密码'},
  'aliyun.com':{name:'阿里云邮箱',imap:'imap.aliyun.com',smtp:'smtp.aliyun.com',ssl:true,
    tip:'使用登录密码即可(企业邮箱请手动填 imap.mxhichina.com / smtp.mxhichina.com)'},
  'sina.com':{name:'新浪邮箱',imap:'imap.sina.com',smtp:'smtp.sina.com',ssl:true,tip:'需在网页版设置里开启 IMAP/SMTP'},
  'sohu.com':{name:'搜狐邮箱',imap:'imap.sohu.com',smtp:'smtp.sohu.com',ssl:true,tip:'需在网页版设置里开启 IMAP/SMTP'},
  '139.com':{name:'移动 139',imap:'imap.139.com',smtp:'smtp.139.com',ssl:true,tip:'需使用客户端授权密码'},
  'icloud.com':{name:'iCloud',imap:'imap.mail.me.com',smtp:'smtp.mail.me.com',ssl:false,smtpPort:587,
    tip:'需使用 App 专用密码(appleid.apple.com);SMTP 走 587'},
  'me.com':{name:'iCloud',imap:'imap.mail.me.com',smtp:'smtp.mail.me.com',ssl:false,smtpPort:587,
    tip:'需使用 App 专用密码;SMTP 走 587'},
};
function detectProvider(addr){const at=addr.indexOf('@');
  return at>0?PROVIDERS[addr.slice(at+1).toLowerCase().trim()]:null}
function smartSMTP(){
  const p=detectProvider($('s-user').value);
  if(!p){$('s-tip').textContent=$('s-user').value.includes('@')?'未识别的服务商,请手动填服务器地址(通常是 smtp.域名)':'';return}
  if(!$('s-host').value){$('s-host').value=p.smtp;
    $('s-port').value=p.smtpPort||465;$('s-ssl').checked=p.ssl!==false}
  $('s-tip').innerHTML='💡 已识别 <b>'+p.name+'</b>,服务器已自动填好。'+esc(p.tip);
}
function copyFromIMAP(){
  const mb=(cfg.mailboxes||[])[0];
  if(!mb)return toast('还没有配置收信邮箱','err');
  $('s-user').value=mb.user||'';$('s-pass').value=mb.password||'';
  $('s-host').value='';smartSMTP();
  if(!$('s-host').value){$('s-host').value=(mb.host||'').replace(/^imap\./,'smtp.');
    $('s-port').value=465;$('s-ssl').checked=true}
  toast('已复制第一个收信邮箱的账号,请确认服务器与端口','ok')}

/* ---------- 收信邮箱 ---------- */
function renderMailboxes(){
  $('mailboxes').innerHTML='';
  (cfg.mailboxes=cfg.mailboxes||[]).forEach((mb,idx)=>{
    const div=document.createElement('div');div.className='entity';
    div.innerHTML=`<div class="head">
        <span class="dot off" data-mbdot="${esc(mb.name||'')}"></span>
        <input type="text" value="${esc(mb.name||'')}" placeholder="邮箱名称(如:主邮箱)" class="f-name">
        <button class="btn f-test">🔌 测试</button>
        <button class="btn danger f-del">删除</button></div>
      <div class="grid2">
        <div><label>邮箱账号</label><input type="text" class="f-user" value="${esc(mb.user||'')}" placeholder="xxx@qq.com(自动识别服务商)"></div>
        <div><label>密码 / 授权码</label><input type="password" class="f-pass" value="${esc(mb.password||'')}"></div>
        <div><label>服务器</label><input type="text" class="f-host" list="imap-hosts" value="${esc(mb.host||'')}"></div>
        <div><label>端口</label><input type="number" class="f-port" value="${mb.port||993}"></div>
        <div><label>文件夹</label><div style="display:flex;gap:6px">
          <select class="f-folder"><option>${esc(mb.folder||'INBOX')}</option></select>
          <button class="btn f-folders" title="连接邮箱获取文件夹列表">🔄</button></div></div>
        <div><label>兜底轮询间隔(秒)</label><input type="number" class="f-poll" value="${mb.poll_interval_sec||60}"></div>
      </div>
      <div class="hint f-tip"></div>
      <label class="switch"><input type="checkbox" class="f-idle" ${mb.idle?'checked':''}> IDLE 实时推送(推荐)</label>
      <label class="switch"><input type="checkbox" class="f-seen" ${mb.mark_seen?'checked':''}> 转发后将原邮件标记为已读</label>`;
    const q=sel=>div.querySelector(sel);
    q('.f-name').oninput=e=>mb.name=e.target.value;
    q('.f-user').oninput=e=>{mb.user=e.target.value;
      const p=detectProvider(mb.user),tip=q('.f-tip');
      if(p){if(!q('.f-host').value){q('.f-host').value=p.imap;q('.f-port').value=993;mb.host=p.imap;mb.port=993}
        if(!q('.f-name').value){q('.f-name').value=p.name;mb.name=p.name}
        tip.innerHTML='💡 已识别 <b>'+p.name+'</b>,服务器已自动填好。'+esc(p.tip)}
      else tip.textContent=mb.user.includes('@')?'未识别的服务商,请手动填服务器(通常是 imap.域名)':''};
    q('.f-pass').oninput=e=>mb.password=e.target.value;
    q('.f-host').oninput=e=>mb.host=e.target.value;
    q('.f-port').oninput=e=>mb.port=+e.target.value||993;
    q('.f-folder').onchange=e=>mb.folder=e.target.value;
    q('.f-folders').onclick=async e=>{const b=e.target;b.disabled=true;b.textContent='…';
      try{
        const r=await api('/api/imap/folders',{method:'POST',body:JSON.stringify(mb)});
        const sel=q('.f-folder'),cur=mb.folder||'INBOX';
        sel.innerHTML=r.folders.map(f=>`<option ${f===cur?'selected':''}>${esc(f)}</option>`).join('');
        if(!r.folders.includes(cur)){sel.insertAdjacentHTML('afterbegin',`<option selected>${esc(cur)}</option>`)}
        toast('已获取 '+r.folders.length+' 个文件夹','ok');
      }catch(err){toast(err.message,'err')}
      b.disabled=false;b.textContent='🔄'};
    q('.f-poll').oninput=e=>mb.poll_interval_sec=+e.target.value||60;
    q('.f-idle').onchange=e=>mb.idle=e.target.checked;
    q('.f-seen').onchange=e=>mb.mark_seen=e.target.checked;
    q('.f-del').onclick=()=>{if(confirm('删除邮箱「'+(mb.name||mb.user||'')+'」的监听?'))
      {cfg.mailboxes.splice(idx,1);renderMailboxes();fillMailboxSelects()}};
    q('.f-test').onclick=async e=>{e.target.disabled=true;
      try{const r=await api('/api/test/imap',{method:'POST',body:JSON.stringify(mb)});toast(r.ok,'ok')}
      catch(err){toast(err.message,'err')}e.target.disabled=false};
    addEye(q('.f-pass'));
    $('mailboxes').appendChild(div);
  });
  updateMbDots();
}
function addMailbox(){cfg.mailboxes.push({name:'',host:'',port:993,user:'',password:'',
  folder:'INBOX',poll_interval_sec:300,idle:true,mark_seen:false});renderMailboxes()}

function fillMailboxSelects(){
  const names=(cfg.mailboxes||[]).map(m=>m.name||m.user).filter(Boolean);
  $('t-mailbox').innerHTML='<option value="">(任意邮箱)</option>'+names.map(n=>`<option>${esc(n)}</option>`).join('');
  $('h-mailbox').innerHTML='<option value="">全部邮箱</option>'+names.map(n=>`<option>${esc(n)}</option>`).join('');
}

/* ---------- 规则 ---------- */
function renderRules(){
  $('rules').innerHTML='';
  const mbNames=(cfg.mailboxes||[]).map(m=>m.name||m.user).filter(Boolean);
  cfg.rules.forEach((r,idx)=>{
    const div=document.createElement('div');div.className='entity'+(r.disabled?' off':'');
    div.innerHTML=`<div class="head">
        <span class="ord" title="规则从上到下匹配,第一条命中生效">#${idx+1}</span>
        <input type="text" value="${esc(r.name)}" placeholder="规则名称">
        <button class="btn f-up" title="上移(优先级更高)" ${idx===0?'disabled':''}>↑</button>
        <button class="btn f-down" title="下移" ${idx===cfg.rules.length-1?'disabled':''}>↓</button>
        <label class="switch" style="margin:0" title="停用后不参与匹配"><input type="checkbox" class="f-en" ${r.disabled?'':'checked'}> 启用</label>
        <button class="btn danger">删除</button></div>
      <label>生效邮箱(留空=全部监听邮箱)</label><div class="c-mb"></div><div class="sugg s-mb"></div>
      <label>发件人包含(任一命中;留空=任意发件人)</label><div class="c-from"></div>
      <label>主题/正文关键词(任一命中;留空=全部转发)</label><div class="c-kw"></div>
      <label>转发到</label><div class="c-to"></div>
      <label class="switch"><input type="checkbox" class="f-attach" ${r.attach_original?'checked':''}> 转发时附带原始邮件 (.eml)</label>`;
    div.querySelector('input[type=text]').oninput=e=>r.name=e.target.value;
    div.querySelector('.f-up').onclick=()=>{
      [cfg.rules[idx-1],cfg.rules[idx]]=[cfg.rules[idx],cfg.rules[idx-1]];renderRules()};
    div.querySelector('.f-down').onclick=()=>{
      [cfg.rules[idx+1],cfg.rules[idx]]=[cfg.rules[idx],cfg.rules[idx+1]];renderRules()};
    div.querySelector('.f-en').onchange=e=>{r.disabled=!e.target.checked;renderRules()};
    div.querySelector('.f-attach').onchange=e=>r.attach_original=e.target.checked;
    div.querySelector('.btn.danger').onclick=()=>{cfg.rules.splice(idx,1);renderRules()};
    r.mailboxes=r.mailboxes||[];r.from_contains=r.from_contains||[];r.keywords=r.keywords||[];r.forward_to=r.forward_to||[];
    const mbChips=makeChips(div.querySelector('.c-mb'),r.mailboxes);
    const sugg=div.querySelector('.s-mb');
    sugg.innerHTML=mbNames.map(n=>`<span class="s">＋ ${esc(n)}</span>`).join('');
    sugg.querySelectorAll('.s').forEach((el,i)=>el.onclick=()=>mbChips.add(mbNames[i]));
    makeChips(div.querySelector('.c-from'),r.from_contains);
    makeChips(div.querySelector('.c-kw'),r.keywords);
    makeChips(div.querySelector('.c-to'),r.forward_to,isEmail);
    $('rules').appendChild(div);
  });
}
function addRule(){cfg.rules.push({name:'新规则',disabled:false,mailboxes:[],from_contains:[],keywords:[],forward_to:[],attach_original:false});renderRules()}

async function aiRule(){
  const desc=$('ai-desc').value.trim();
  if(!desc)return toast('先描述一下规则','err');
  if(!$('a-url').value.trim())return toast('请先在「系统设置 → AI 助手」里配置接口','err');
  const btn=$('ai-btn');btn.disabled=true;btn.textContent='生成中…';$('ai-hint').textContent='';
  try{
    collectConfig();await api('/api/config',{method:'PUT',body:JSON.stringify(cfg)});
    const r=await api('/api/ai/rule',{method:'POST',body:JSON.stringify({desc})});
    cfg.rules.push(r);renderRules();
    $('ai-hint').textContent='已生成规则「'+(r.name||'')+'」,请核对后点底部保存';
    toast('规则已生成,记得保存','ok');
  }catch(e){toast(e.message,'err')}
  btn.disabled=false;btn.textContent='生成规则';
}

async function testRule(){
  collectConfig();
  try{
    const r=await api('/api/test/rule',{method:'POST',body:JSON.stringify({
      rules:cfg.rules,mailbox:$('t-mailbox').value,from:$('t-from').value,
      subject:$('t-subject').value,body:$('t-body').value})});
    $('t-result').innerHTML=r.matched
      ?'<span class="b-ok">✅ 命中规则「'+esc(r.name)+'」</span>'
      :'<span class="b-warn">⚠️ 不命中任何规则,不会转发</span>';
  }catch(e){toast(e.message,'err')}
}

async function testE2E(){
  if(!confirm('将用已保存的配置真实发送一封转发邮件到命中规则的目标邮箱,继续?\n(如果刚改过规则,请先点底部「保存并应用」)'))return;
  const btn=$('e2e-btn');btn.disabled=true;btn.textContent='测试中…';$('e2e-result').innerHTML='';
  try{
    const r=await api('/api/test/e2e',{method:'POST',body:JSON.stringify({
      mailbox:$('t-mailbox').value,from:$('t-from').value,
      subject:$('t-subject').value,body:$('t-body').value})});
    if(!r.matched){
      $('e2e-result').innerHTML='<div class="deliv"><span class="b-warn">⚠️ 不命中任何已保存的规则,链路终止(未发信)</span></div>';
    }else{
      $('e2e-result').innerHTML='<div class="deliv">✅ 命中规则「'+esc(r.name)+'」</div>'+
        (r.results||[]).map(x=>`<div class="deliv">
          ${x.ok?'<span class="b-ok">✅ 已送达</span>':'<span class="b-err">❌ 发送失败</span>'}
          → ${esc(x.target)} <span style="color:var(--dim)">(${x.action})</span>
          ${x.error?'<div class="b-err" style="margin-top:4px">'+esc(x.error)+'</div>':''}</div>`).join('');
      toast(r.results&&r.results.every(x=>x.ok)?'全链路测试通过,去目标邮箱查收':'链路有失败环节,看下方详情',
        r.results&&r.results.every(x=>x.ok)?'ok':'err');
    }
  }catch(e){toast(e.message,'err')}
  btn.disabled=false;btn.textContent='🚀 真实转发测试';
}

/* ---------- 配置读写 ---------- */
async function loadConfig(){
  cfg=await api('/api/config');
  cfg.mailboxes=cfg.mailboxes||[];cfg.rules=cfg.rules||[];
  renderMailboxes();renderRules();fillMailboxSelects();
  $('s-host').value=cfg.smtp.host||'';$('s-port').value=cfg.smtp.port||465;
  $('s-user').value=cfg.smtp.user||'';$('s-pass').value=cfg.smtp.password||'';
  $('s-from').value=cfg.smtp.from||'';$('s-ssl').checked=!!cfg.smtp.ssl;
  $('a-url').value=cfg.ai.base_url||'';$('a-key').value=cfg.ai.api_key||'';$('a-model').value=cfg.ai.model||'';
}
function collectConfig(){
  // mailboxes/rules 已通过输入事件实时绑定到 cfg
  cfg.smtp={host:$('s-host').value.trim(),port:+$('s-port').value||465,ssl:$('s-ssl').checked,
    user:$('s-user').value.trim(),password:$('s-pass').value,from:$('s-from').value.trim()};
  cfg.ai={base_url:$('a-url').value.trim(),api_key:$('a-key').value,model:$('a-model').value.trim()};
}
async function saveConfig(){
  collectConfig();
  try{await api('/api/config',{method:'PUT',body:JSON.stringify(cfg)});
    toast('已保存并热生效','ok');fillMailboxSelects();refreshStatus()}
  catch(e){toast(e.message,'err')}
}
async function testSMTP(to){collectConfig();const b=$('ts-btn');b.disabled=true;
  try{const r=await api('/api/test/smtp',{method:'POST',
    body:JSON.stringify(Object.assign({},cfg.smtp,{to:(to||'').trim()}))});toast(r.ok,'ok')}
  catch(e){toast(e.message,'err')}b.disabled=false}

/* ---------- 启动 ---------- */
addEye($('s-pass'));addEye($('a-key'));addEye($('p-old'));addEye($('p-new'));
$('s-user').addEventListener('input',smartSMTP);
$('s-ssl').addEventListener('change',()=>{
  const p=+$('s-port').value;
  if($('s-ssl').checked&&(p===587||!p))$('s-port').value=465;
  if(!$('s-ssl').checked&&(p===465||!p))$('s-port').value=587});
(async()=>{try{await api('/api/status');showApp()}catch(e){showLogin()}})();