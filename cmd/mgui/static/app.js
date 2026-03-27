let currentTab = 'chat';
let providers = [];

// Initialize
async function init() {
    loadSettings();
    await probe();
    await loadProviders();
    await updateStatus();
    setInterval(updateStatus, 10000);
}

function saveSettings() {
    const settings = {
        email: document.getElementById('settings-email').value,
        mode: document.getElementById('settings-mode').value,
        priority: document.getElementById('settings-priority').value
    };
    localStorage.setItem('mgui_settings', JSON.stringify(settings));
    alert('Settings saved!');
}

function loadSettings() {
    const saved = localStorage.getItem('mgui_settings');
    if (saved) {
        const s = JSON.parse(saved);
        if (document.getElementById('settings-email')) document.getElementById('settings-email').value = s.email || '';
        if (document.getElementById('settings-mode')) document.getElementById('settings-mode').value = s.mode || 'sync';
        if (document.getElementById('settings-priority')) document.getElementById('settings-priority').value = s.priority || '5';
    }
}

async function probe() {
    const statusEl = document.getElementById('probe-status');
    const guideEl = document.getElementById('ollama-guide');
    const joinBtn = document.getElementById('join-btn');

    try {
        const resp = await fetch('/api/probe');
        const data = await resp.json();
        
        statusEl.textContent = `${data.gpu} · Ollama ${data.ollama}`;
        
        if (data.ollama !== 'Running') {
            guideEl.style.display = 'block';
            joinBtn.style.display = 'none';
        } else if (!data.models || data.models.length === 0) {
            guideEl.style.display = 'block';
            guideEl.innerHTML = `<h3 style="color:#c53030">No Models Pulled</h3>
                                 <p>Ollama is running, but no models are available.</p>
                                 <code>ollama pull llama3</code>`;
            joinBtn.style.display = 'none';
        } else {
            guideEl.style.display = 'none';
            joinBtn.style.display = 'inline-block';
        }
    } catch (e) {
        statusEl.textContent = 'Failed to probe';
    }
}

function showTab(tabId) {
    document.querySelectorAll('.tab').forEach(t => t.style.display = 'none');
    document.querySelectorAll('nav button').forEach(b => b.classList.remove('active'));
    
    document.getElementById('tab-' + tabId).style.display = 'block';
    document.getElementById('btn-' + tabId).classList.add('active');
    currentTab = tabId;
}

async function loadProviders() {
    const resp = await fetch('/api/providers');
    providers = await resp.json();
    
    const select = document.getElementById('provider-select');
    select.innerHTML = '<option value="">Select Provider</option>';
    providers.forEach(p => {
        const opt = document.createElement('option');
        opt.value = p.id;
        opt.textContent = p.name;
        select.appendChild(opt);
    });
}

function updateModels() {
    const pId = document.getElementById('provider-select').value;
    const provider = providers.find(p => p.id === pId);
    const select = document.getElementById('model-select');
    select.innerHTML = '<option value="">Select Model</option>';
    
    if (provider && provider.models) {
        provider.models.forEach(m => {
            const opt = document.createElement('option');
            opt.value = m.id;
            opt.textContent = m.name;
            select.appendChild(opt);
        });
    }
}

async function sendMessage() {
    const provider = document.getElementById('provider-select').value;
    const model = document.getElementById('model-select').value;
    const prompt = document.getElementById('prompt-input').value;
    
    if (!provider || !model || !prompt) return;
    
    const settings = JSON.parse(localStorage.getItem('mgui_settings') || '{}');
    const isAsync = settings.mode === 'async' && provider === 'momagrid';
    const endpoint = isAsync ? '/api/hub/jobs' : `/api/chat?provider=${provider}`;

    const history = document.getElementById('chat-history');
    history.innerHTML += `<div class="message user"><strong>You:</strong> ${prompt}</div>`;
    document.getElementById('prompt-input').value = '';
    
    try {
        const payload = { 
            model, 
            prompt, 
            max_tokens: 1024, 
            temperature: 0.7 
        };
        if (isAsync) {
            payload.notify = { email: settings.email };
            payload.priority = parseInt(settings.priority || "5");
        }

        const resp = await fetch(endpoint, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        });
        
        const data = await resp.json();
        if (data.error || data.detail) {
            history.innerHTML += `<div class="message ai" style="color:red"><strong>Error:</strong> ${data.error || data.detail}</div>`;
        } else if (isAsync) {
            history.innerHTML += `<div class="message ai"><strong>Job Submitted:</strong> ${data.job_id} (Tracking in Grid Status)</div>`;
        } else {
            history.innerHTML += `<div class="message ai"><strong>AI:</strong> ${data.content}</div>`;
        }
    } catch (e) {
        history.innerHTML += `<div class="message ai" style="color:red"><strong>Error:</strong> ${e.message}</div>`;
    }
    history.scrollTop = history.scrollHeight;
}

async function startOnboarding() {
    const stepsDiv = document.getElementById('onboarding-steps');
    stepsDiv.innerHTML = '';
    
    const evts = new EventSource('/api/join');
    evts.onmessage = (e) => {
        const data = JSON.parse(e.data);
        const step = document.createElement('div');
        step.textContent = `✓ ${data.step}`;
        stepsDiv.appendChild(step);
        
        if (data.step === 'ONLINE') {
            evts.close();
            document.getElementById('join-btn').textContent = 'Joined Grid';
            document.getElementById('join-btn').disabled = true;
        }
    };
    evts.onerror = () => evts.close();
}

async function updateStatus() {
    try {
        const agentsResp = await fetch('/api/hub/agents');
        const agentsData = await agentsResp.json();
        const agentsTable = document.querySelector('#agents-table tbody');
        agentsTable.innerHTML = '';
        if (agentsData.agents) {
            agentsData.agents.forEach(a => {
                const row = agentsTable.insertRow();
                row.insertCell(0).textContent = a.name || a.agent_id;
                row.insertCell(1).textContent = a.tier;
                row.insertCell(2).textContent = a.status;
                row.insertCell(3).textContent = a.current_tps.toFixed(1);
            });
        }

        const tasksResp = await fetch('/api/hub/tasks?limit=10');
        const tasksData = await tasksResp.json();
        const tasksTable = document.querySelector('#tasks-table tbody');
        tasksTable.innerHTML = '';
        if (tasksData.tasks) {
            tasksData.tasks.forEach(t => {
                const row = tasksTable.insertRow();
                row.insertCell(0).textContent = t.task_id.substring(0, 8);
                row.insertCell(1).textContent = t.model;
                row.insertCell(2).textContent = t.state;
                row.insertCell(3).textContent = t.latency_ms ? `${(t.latency_ms/1000).toFixed(1)}s` : '-';
            });
        }

        const jobsResp = await fetch('/api/hub/jobs?limit=10');
        const jobsData = await jobsResp.json();
        const jobsTable = document.querySelector('#jobs-table tbody');
        jobsTable.innerHTML = '';
        if (jobsData.jobs) {
            jobsData.jobs.forEach(j => {
                const row = jobsTable.insertRow();
                row.insertCell(0).textContent = j.job_id;
                row.insertCell(1).textContent = j.model;
                row.insertCell(2).textContent = j.state;
                row.insertCell(3).textContent = new Date(j.created_at).toLocaleTimeString();
                row.insertCell(4).textContent = new Date(j.updated_at).toLocaleTimeString();
            });
        }
    } catch (e) {
        console.error('Failed to update status', e);
    }
}

init();
