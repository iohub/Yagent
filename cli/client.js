#!/usr/bin/env node

const { Command } = require('commander');
const chalk = require('chalk');
const WebSocket = require('ws');
const http = require('http');
const https = require('https');
const { URL } = require('url');
const readline = require('readline');
const util = require('util');

// UI helpers
const symbols = {
  rocket: '🚀',
  success: '✅',
  error: '❌',
  warn: '⚠️',
  info: 'ℹ️',
  ws: '🔌',
  ai: '🤖',
  user: '💬',
  mem: '🧠',
  done: '🎯'
};

let activeReadline = null;
function setActiveReadline(rl) {
  activeReadline = rl;
}

function safeWrite(stream, text) {
  if (activeReadline) {
    try {
      stream.clearLine(0);
      stream.cursorTo(0);
    } catch (_) {}
    stream.write(text + '\n');
    try {
      activeReadline.prompt(true);
    } catch (_) {}
  } else {
    stream.write(text + '\n');
  }
}

// Patch console to avoid breaking the input prompt
const _origLog = console.log.bind(console);
const _origErr = console.error.bind(console);
console.log = (...args) => {
  const msg = util.format(...args);
  safeWrite(process.stdout, msg);
};
console.error = (...args) => {
  const msg = util.format(...args);
  safeWrite(process.stderr, msg);
};

function ts() {
  const d = new Date();
  return chalk.gray(dim(`[${d.toLocaleTimeString()}]`));
}

function dim(text) {
  return chalk.gray(text);
}

function hr(width = 60) {
  return chalk.gray('─'.repeat(width));
}

function header(title) {
  const pad = 2;
  const line = ' '.repeat(pad) + title + ' '.repeat(pad);
  const top = '┌' + '─'.repeat(line.length) + '┐';
  const mid = '│' + line + '│';
  const bot = '└' + '─'.repeat(line.length) + '┘';
  return chalk.cyan(top) + '\n' + chalk.cyan(mid) + '\n' + chalk.cyan(bot);
}

function keyVal(key, val) {
  const k = chalk.gray(key.padEnd(6));
  const v = chalk.white(val);
  return `${k} ${v}`;
}

function banner(lines) {
  const content = lines.map((l) => `  ${l}`).join('\n');
  const top = chalk.blueBright('╭' + '─'.repeat(58) + '╮');
  const bottom = chalk.blueBright('╰' + '─'.repeat(58) + '╯');
  return `${top}\n${content}\n${bottom}`;
}

function badge(text, color = chalk.blueBright) {
  return color.inverse(` ${text} `);
}

function printHelpCommands() {
  const cmds = [
    `${badge('CMD')} ${chalk.white('/memory')} ${dim('view memory')}`,
    `${badge('CMD')} ${chalk.white('/clear')} ${dim('clear memory')}`,
    `${badge('CMD')} ${chalk.white('/quit')} ${dim('exit client')}`
  ];
  console.log(header('Commands'));
  cmds.forEach((c) => console.log(c));
}

function httpRequest(method, urlString, body) {
  return new Promise((resolve, reject) => {
    const url = new URL(urlString);
    const isHttps = url.protocol === 'https:';
    const lib = isHttps ? https : http;
    const data = body ? Buffer.from(JSON.stringify(body)) : null;
    const req = lib.request({
      hostname: url.hostname,
      port: url.port || (isHttps ? 443 : 80),
      path: url.pathname + (url.search || ''),
      method,
      headers: {
        'Content-Type': 'application/json',
        'Content-Length': data ? data.length : 0
      }
    }, (res) => {
      let chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => {
        const text = Buffer.concat(chunks).toString('utf8');
        try {
          const json = text ? JSON.parse(text) : {};
          if (res.statusCode && res.statusCode >= 200 && res.statusCode < 300) {
            resolve(json);
          } else {
            reject(new Error(json.error || `HTTP ${res.statusCode}: ${text}`));
          }
        } catch (e) {
          if (res.statusCode && res.statusCode >= 200 && res.statusCode < 300) {
            resolve({ raw: text });
          } else {
            reject(new Error(`HTTP ${res.statusCode}: ${text}`));
          }
        }
      });
    });
    req.on('error', reject);
    if (data) req.write(data);
    req.end();
  });
}

function toWsUrl(httpBase) {
  const base = new URL(httpBase);
  base.protocol = base.protocol === 'https:' ? 'wss:' : 'ws:';
  base.pathname = '/ws';
  base.search = '';
  return base.toString();
}

class HttpWsClient {
  constructor({ httpBase, projectDir, taskDesc, pollIntervalSec, dev }) {
    this.httpBase = httpBase.replace(/\/?$/, '');
    this.projectDir = projectDir;
    this.taskDesc = taskDesc;
    this.pollIntervalSec = pollIntervalSec;
    this.dev = dev;
    this.taskId = null;
    this.ws = null;
    this.pollTimer = null;
    this.lastPrintedMessageCount = 0;
    this.lastStatusText = null;
    this.rl = null;
  }

  async start() {
    const title = `${symbols.rocket} DeepCoder Client`;
    const http = keyVal('HTTP', chalk.blueBright(this.httpBase));
    const ws = keyVal('WS', chalk.blueBright(toWsUrl(this.httpBase)));
    _origLog(banner([chalk.green.bold(` ${title}`), http, ws]));
    _origLog(dim(hr()));

    await this.submitTask();
    await this.connectWs();
    this.startPolling();
    this.setupStdin();
  }

  async submitTask() {
    const url = `${this.httpBase}/api/start_task`;
    console.log(`${ts()} ${chalk.blueBright(symbols.rocket)} ${chalk.blue('Submitting task')} ${dim('via HTTP')}`);
    console.log(`${badge('TASK')} ${chalk.white(this.taskDesc)}`);
    const res = await httpRequest('POST', url, {
      project_dir: this.projectDir,
      task_desc: this.taskDesc
    });
    this.taskId = res.task_id;
    console.log(`${ts()} ${chalk.green(symbols.done)} ${chalk.green('Task created')} ${dim(`#${this.taskId}`)}`);
    console.log(dim(hr()));
  }

  async connectWs() {
    const wsUrl = toWsUrl(this.httpBase);
    this.ws = new WebSocket(wsUrl);
    this.ws.on('open', () => {
      console.log(`${ts()} ${chalk.green(symbols.success)} ${chalk.green('WS connected')} ${dim(wsUrl)}`);
    });
    this.ws.on('close', (code, reason) => {
      console.log(`${ts()} ${chalk.yellow(symbols.ws)} ${chalk.yellow('WS closed')} ${dim(`${code} ${reason || ''}`)}`);
    });
    this.ws.on('error', (err) => {
      console.log(`${ts()} ${chalk.red(symbols.error)} ${chalk.red('WS error')} ${dim(err.message)}`);
    });
    this.ws.on('message', (data) => {
      try {
        const msg = JSON.parse(data.toString());
        if (this.dev) console.log(dim(`${symbols.info} WS ${JSON.stringify(msg)}`));
        if (msg.type === 'chat_message' && msg.event === 'ai_response' && msg.data && msg.data.content) {
          console.log(`${ts()} ${chalk.magenta(symbols.ai)} ${chalk.magenta(msg.data.content)}`);
        }
        if (msg.type === 'realtime' && msg.data && msg.data.task_id === this.taskId) {
          console.log(`${ts()} ${chalk.cyan(symbols.info)} ${chalk.cyan(msg.event)} ${dim(msg.data.content)}`);
        }
      } catch (e) {
        console.log(`${ts()} ${chalk.red(symbols.error)} ${chalk.red('Failed to parse WS message')}`);
      }
    });
  }

  startPolling() {
    const statusUrl = `${this.httpBase}/api/task_status?task_id=${encodeURIComponent(this.taskId)}`;
    const memoryUrl = `${this.httpBase}/api/memory?task_id=${encodeURIComponent(this.taskId)}`;
    this.pollTimer = setInterval(async () => {
      try {
        const status = await httpRequest('GET', statusUrl);
        if (status && status.status) {
          const statusText = `${symbols.info} ${chalk.white('Status')} ${chalk.gray(':')} ${chalk.white(status.status)}${status.progress ? chalk.gray(` (${status.progress}%)`) : ''}`;
          if (this.lastStatusText !== statusText) {
            console.log(`${ts()} ${dim(statusText)}`);
            this.lastStatusText = statusText;
          }
          if (status.status === 'finished') {
            console.log(`${ts()} ${chalk.green(symbols.success)} ${chalk.green('Task finished')}. ${dim('You can continue chatting or start a new task.')}`);
            // Don't exit, allow continued conversation
            return;
          }
        }
      } catch (e) {
        if (this.dev) console.log(chalk.red(`Status poll failed: ${e.message}`));
      }
      try {
        const mem = await httpRequest('GET', memoryUrl);
        const messages = Array.isArray(mem.messages) ? mem.messages : [];
        if (messages.length > this.lastPrintedMessageCount) {
          const newMsgs = messages.slice(this.lastPrintedMessageCount);
          for (const m of newMsgs) {
            if ((m.type || m.Type) === 'assistant' && (m.content || m.Content)) {
              console.log(`${ts()} ${chalk.magenta(symbols.ai)} ${chalk.magenta(m.content || m.Content)}`);
            }
          }
          this.lastPrintedMessageCount = messages.length;
        }
      } catch (e) {
        if (this.dev) console.log(chalk.red(`Memory poll failed: ${e.message}`));
      }
    }, this.pollIntervalSec * 1000);
  }

  sendChat(text) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      console.log(`${ts()} ${chalk.red(symbols.error)} ${chalk.red('WS not connected')}`);
      return;
    }
    const payload = {
      type: 'chat_message',
      event: 'chat_message',
      data: { task_id: this.taskId, message: text }
    };
    console.log(`${ts()} ${chalk.blue(symbols.user)} ${chalk.blue('You')}: ${chalk.white(text)}`);
    this.ws.send(JSON.stringify(payload));
  }

  setupStdin() {
    console.log(header('Interactive Chat'));
    console.log(`${dim('Type to chat, or use commands below')}`);
    printHelpCommands();
    console.log(dim(hr()));

    this.rl = readline.createInterface({
      input: process.stdin,
      output: process.stdout,
      prompt: chalk.cyan('> ')
    });
    setActiveReadline(this.rl);

    this.rl.prompt();

    this.rl.on('line', async (lineRaw) => {
      const line = (lineRaw || '').trim();
      if (!line) {
        this.rl.prompt();
        return;
      }
      if (line === '/quit' || line === '/exit') {
        this.cleanupAndExit();
        return;
      }
      if (line === '/memory') {
        try {
          const mem = await httpRequest('GET', `${this.httpBase}/api/memory?task_id=${encodeURIComponent(this.taskId)}`);
          console.log(header('Memory'));
          console.log(chalk.yellow(JSON.stringify(mem, null, 2)));
          console.log(dim(hr()));
        } catch (e) {
          console.log(`${ts()} ${chalk.red(symbols.error)} ${chalk.red(e.message)}`);
        }
        this.rl.prompt();
        return;
      }
      if (line === '/clear') {
        try {
          await httpRequest('DELETE', `${this.httpBase}/api/memory?task_id=${encodeURIComponent(this.taskId)}`);
          console.log(`${ts()} ${chalk.yellow(symbols.mem)} ${chalk.yellow('Memory cleared.')}`);
          this.lastPrintedMessageCount = 0;
        } catch (e) {
          console.log(`${ts()} ${chalk.red(symbols.error)} ${chalk.red(e.message)}`);
        }
        this.rl.prompt();
        return;
      }
      this.sendChat(line);
      this.rl.prompt();
    });

    this.rl.on('SIGINT', () => {
      this.cleanupAndExit();
    });
  }

  cleanupAndExit() {
    if (this.pollTimer) clearInterval(this.pollTimer);
    if (this.ws) {
      try {
        this.ws.close();
      } catch (_) {}
    }
    if (this.rl) {
      try {
        this.rl.close();
      } catch (_) {}
    }
    setActiveReadline(null);
    console.log(chalk.green(`${symbols.success} Goodbye!`));
    process.exit(0);
  }
}

(async () => {
  const program = new Command();
  program
    .name('deepcoder-client')
    .description('Submit task via HTTP, print agent messages, and reply via WebSocket')
    .option('--http <url>', 'HTTP server base URL', 'http://localhost:9080')
    .option('--project <dir>', 'Project directory', process.cwd())
    .option('--task <desc>', 'Task description', '')
    .option('--interval <sec>', 'Polling interval seconds', (v) => parseInt(v, 10), 2)
    .option('--dev', 'Verbose logging', false)
    .parse(process.argv);

  const opts = program.opts();
  if (!opts.task) {
    console.log(chalk.red('❌ Please provide --task'));
    process.exit(1);
  }

  const client = new HttpWsClient({
    httpBase: opts.http,
    projectDir: opts.project,
    taskDesc: opts.task,
    pollIntervalSec: opts.interval,
    dev: !!opts.dev
  });
  try {
    await client.start();
  } catch (e) {
    console.error(chalk.red(`❌ ${e.message}`));
    process.exit(1);
  }
})(); 
