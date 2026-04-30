const http = require('http');
const WebSocket = require('ws');

class CodeActorClient {
  constructor(host = 'localhost', port = 9080) {
    this.host = host;
    this.port = port;
    this.baseUrl = `http://${host}:${port}`;
    this.wsUrl = `ws://${host}:${port}/ws`;
  }

  // ── HTTP helpers ──────────────────────────────────────────

  _request(method, path, body = null) {
    return new Promise((resolve, reject) => {
      const url = new URL(path, this.baseUrl);
      const options = {
        method,
        hostname: url.hostname,
        port: url.port,
        path: url.pathname + url.search,
        headers: { 'Content-Type': 'application/json' },
      };

      const req = http.request(options, (res) => {
        let data = '';
        res.on('data', (chunk) => (data += chunk));
        res.on('end', () => {
          try {
            const json = JSON.parse(data);
            if (res.statusCode >= 400) {
              reject(new Error(json.error || `HTTP ${res.statusCode}`));
            } else {
              resolve(json);
            }
          } catch {
            reject(new Error(`Invalid JSON response: ${data}`));
          }
        });
      });

      req.on('error', (err) => {
        reject(new Error(`Cannot connect to ${this.host}:${this.port} — ${err.message}`));
      });

      if (body) {
        req.write(JSON.stringify(body));
      }
      req.end();
    });
  }

  // ── HTTP API ──────────────────────────────────────────────

  /** POST /api/start_task */
  async createTask(projectDir, taskDesc, taskId = null) {
    const body = { project_dir: projectDir, task_desc: taskDesc };
    if (taskId) body.task_id = taskId;
    return this._request('POST', '/api/start_task', body);
  }

  /** GET /api/task_status?task_id=xxx */
  async getTaskStatus(taskId) {
    return this._request('GET', `/api/task_status?task_id=${encodeURIComponent(taskId)}`);
  }

  /** POST /api/cancel_task */
  async cancelTask(taskId) {
    return this._request('POST', '/api/cancel_task', { task_id: taskId });
  }

  /** GET /api/history */
  async getHistory() {
    return this._request('GET', '/api/history');
  }

  /** GET /api/memory?task_id=xxx */
  async getMemory(taskId) {
    return this._request('GET', `/api/memory?task_id=${encodeURIComponent(taskId)}`);
  }

  /** DELETE /api/memory?task_id=xxx */
  async clearMemory(taskId) {
    return this._request('DELETE', `/api/memory?task_id=${encodeURIComponent(taskId)}`);
  }

  /** POST /api/load_task */
  async loadTask(taskId, projectDir) {
    return this._request('POST', '/api/load_task', { task_id: taskId, project_dir: projectDir });
  }

  // ── WebSocket ─────────────────────────────────────────────

  /**
   * Connect via WebSocket and stream task execution.
   *
   * Returns an object with { ws, taskPromise }.
   * - `ws` is the raw WebSocket for sending follow-up messages
   * - `taskPromise` resolves when the task finishes
   */
  runTask(projectDir, taskDesc, taskId = null) {
    let resolveTask, rejectTask;
    const taskPromise = new Promise((resolve, reject) => {
      resolveTask = resolve;
      rejectTask = reject;
    });

    const id = taskId || this._uuid();

    const ws = new WebSocket(this.wsUrl);
    const messages = [];

    ws.on('open', () => {
      ws.send(JSON.stringify({
        type: 'start_task',
        event: 'start_task',
        data: { project_dir: projectDir, task_desc: taskDesc, task_id: id },
      }));
    });

    ws.on('message', (raw) => {
      try {
        const msg = JSON.parse(raw.toString());
        messages.push(msg);
        this._handleMessage(msg, resolveTask, rejectTask, ws, messages);
      } catch {
        // ignore non-JSON frames
      }
    });

    ws.on('error', (err) => {
      rejectTask(new Error(`WebSocket error: ${err.message}`));
    });

    ws.on('close', () => {
      // if task hasn't resolved yet, resolve with collected messages
      if (!taskPromise._resolved) {
        taskPromise._resolved = true;
        resolveTask({ status: 'disconnected', messages });
      }
    });

    return { ws, taskPromise, taskId: id };
  }

  /**
   * Continue a conversation with an existing task via WebSocket.
   */
  chatWithTask(taskId, projectDir, message) {
    return new Promise((resolve, reject) => {
      const ws = new WebSocket(this.wsUrl);
      const responses = [];

      ws.on('open', () => {
        ws.send(JSON.stringify({
          type: 'chat_message',
          event: 'chat_message',
          data: { task_id: taskId, project_dir: projectDir, message },
        }));
      });

      ws.on('message', (raw) => {
        try {
          const msg = JSON.parse(raw.toString());
          responses.push(msg);
          if (msg.event === 'ai_response' || msg.event === 'error') {
            ws.close();
          }
        } catch { /* ignore */ }
      });

      ws.on('error', reject);
      ws.on('close', () => resolve({ taskId, responses }));
    });
  }

  _handleMessage(msg, resolve, reject, ws, messages) {
    const { event, data, message } = msg;

    switch (event) {
      case 'task_created':
        this._emit('task_created', data);
        this._emit('task_id', data?.task_id);
        break;

      case 'task_update': {
        const { status, result, error, progress } = data || {};
        this._emit('status', { status, result, error, progress });

        if (status === 'finished') {
          // delay close to receive trailing messages
          setTimeout(() => ws.close(), 200);
        } else if (status === 'failed' || status === 'cancelled') {
          setTimeout(() => ws.close(), 200);
        }
        break;
      }

      case 'agent_msg':
        this._emit('agent_msg', typeof data === 'string' ? data : JSON.stringify(data));
        break;

      case 'realtime':
        if (data?.content) {
          this._emit('realtime', data.content, data);
        }
        break;

      case 'chat_message':
      case 'ai_response':
        this._emit('response', data?.content || message);
        break;

      case 'error':
        this._emit('error', message || data?.error || 'Unknown error');
        break;

      // memory events
      case 'memory':
        if (msg.event === 'memory' && data?.messages) {
          this._emit('memory', data);
        }
        break;

      default:
        this._emit('raw', msg);
        break;
    }
  }

  // ── simple event emitter ──────────────────────────────────

  _listeners = {};

  on(event, fn) {
    (this._listeners[event] ||= []).push(fn);
    return this;
  }

  _emit(event, ...args) {
    (this._listeners[event] || []).forEach((fn) => fn(...args));
  }

  _uuid() {
    // simple uuid v4
    return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
      const r = (Math.random() * 16) | 0;
      const v = c === 'x' ? r : (r & 0x3) | 0x8;
      return v.toString(16);
    });
  }
}

module.exports = CodeActorClient;
