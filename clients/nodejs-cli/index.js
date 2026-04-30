#!/usr/bin/env node
'use strict';

const readline = require('readline');
const CodeActorClient = require('./lib/client');

// ── helpers ──────────────────────────────────────────────────

let chalk;
try { chalk = require('chalk'); } catch { chalk = { green: s => s, blue: s => s, yellow: s => s, red: s => s, cyan: s => s, gray: s => s, dim: s => s, bold: s => s }; }

function usage() {
  console.log(`
  ${chalk.bold('CodeActor CLI')} — HTTP + WebSocket client for CodeActor Agent

  ${chalk.cyan('Usage:')}
    codeactor [options] <command> [args]

  ${chalk.cyan('Commands:')}
    ${chalk.bold('run')}      <project-dir> <task-description>   Create a task and stream output via WebSocket
    ${chalk.bold('chat')}     <task-id> <project-dir>            Interactive chat with an existing task
    ${chalk.bold('status')}   <task-id>                          Get task status
    ${chalk.bold('cancel')}   <task-id>                          Cancel a running task
    ${chalk.bold('history')}                                     List recent tasks
    ${chalk.bold('memory')}   <task-id>                          Show conversation memory
    ${chalk.bold('load')}     <task-id> <project-dir>            Load a historical task

  ${chalk.cyan('Options:')}
    --host <host>     Server host (default: localhost)
    --port <port>     Server port (default: 9080)
                      Or set CODECACTOR_HOST=host:port

  ${chalk.cyan('Examples:')}
    codeactor run /home/me/myproject "Add a login endpoint"
    codeactor run --host dev-server --port 9080 ./ "Fix the auth bug"
    codeactor chat 3f8a2b1c-d45e-67f8-9ab0-c1d23e4f5a6b ./myproject
    codeactor status 3f8a2b1c-d45e-67f8-9ab0-c1d23e4f5a6b
    codeactor cancel 3f8a2b1c-d45e-67f8-9ab0-c1d23e4f5a6b
    codeactor history
  `);
}

function parseArgs(argv) {
  const opts = { host: 'localhost', port: 9080 };
  const args = [];

  // check env
  const envHost = process.env.CODECACTOR_HOST;
  if (envHost) {
    const [h, p] = envHost.split(':');
    opts.host = h;
    if (p) opts.port = parseInt(p, 10);
  }

  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--host' && argv[i + 1]) {
      opts.host = argv[++i];
    } else if (argv[i] === '--port' && argv[i + 1]) {
      opts.port = parseInt(argv[++i], 10);
    } else if (argv[i] === '--help' || argv[i] === '-h') {
      usage();
      process.exit(0);
    } else {
      args.push(argv[i]);
    }
  }

  return { opts, args };
}

// ── command handlers ─────────────────────────────────────────

async function cmdRun(client, args) {
  if (args.length < 2) {
    console.error(chalk.red('Usage: codeactor run <project-dir> <task-description>'));
    process.exit(1);
  }
  const projectDir = args[0];
  const taskDesc = args[1];

  console.log(chalk.cyan(`Connecting to ${client.host}:${client.port}...`));
  console.log(chalk.gray(`Project: ${projectDir}`));
  console.log(chalk.gray(`Task: ${taskDesc}`));
  console.log(chalk.dim('─'.repeat(60)));

  const { taskPromise } = client.runTask(projectDir, taskDesc);

  let taskId = null;
  let lastStatus = '';

  client.on('task_id', (id) => {
    taskId = id;
    console.log(chalk.green(`Task created: ${id}`));
  });

  client.on('status', ({ status, result, error, progress }) => {
    if (status !== lastStatus) {
      console.log(chalk.yellow(`\n[${status}]`));
      lastStatus = status;
    }
    if (progress && progress !== 'running') {
      console.log(chalk.gray(`  ${progress}`));
    }
    if (result) {
      console.log(chalk.dim('─'.repeat(60)));
      console.log(chalk.bold('Result:'));
      console.log(result);
    }
    if (error) {
      console.log(chalk.red(`\nError: ${error}`));
    }
  });

  client.on('agent_msg', (content) => {
    process.stdout.write(chalk.blue(content));
  });

  client.on('realtime', (content) => {
    process.stdout.write(chalk.green(content));
  });

  client.on('error', (err) => {
    console.error(chalk.red(`\nError: ${err}`));
  });

  try {
    await taskPromise;
    if (taskId) {
      console.log(chalk.dim(`\nTo continue: codeactor chat ${taskId} ${projectDir}`));
    }
  } catch (err) {
    console.error(chalk.red(`\nFailed: ${err.message}`));
    process.exit(1);
  }
}

async function cmdChat(client, args) {
  if (args.length < 2) {
    console.error(chalk.red('Usage: codeactor chat <task-id> <project-dir>'));
    process.exit(1);
  }
  const taskId = args[0];
  const projectDir = args[1];

  // First check task status
  try {
    const status = await client.getTaskStatus(taskId);
    console.log(chalk.gray(`Task: ${taskId}  Status: ${status.status}`));
  } catch {
    console.log(chalk.yellow(`Task ${taskId} not found in memory, attempting to load...`));
    try {
      await client.loadTask(taskId, projectDir);
      console.log(chalk.green('Task loaded from history.'));
    } catch (err) {
      console.error(chalk.red(`Cannot load task: ${err.message}`));
      process.exit(1);
    }
  }

  const rl = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
    prompt: chalk.cyan('> '),
  });

  console.log(chalk.bold(`Chat with task ${taskId}. Type your message. Ctrl+C to exit.`));
  console.log(chalk.dim('─'.repeat(60)));
  rl.prompt();

  rl.on('line', async (line) => {
    const input = line.trim();
    if (!input) {
      rl.prompt();
      return;
    }
    if (input === '/exit' || input === '/quit') {
      rl.close();
      return;
    }
    if (input === '/memory') {
      try {
        const mem = await client.getMemory(taskId);
        mem.messages.forEach((m) => {
          const label = { system: chalk.gray, human: chalk.cyan, assistant: chalk.green, tool: chalk.yellow }[m.type] || chalk.dim;
          console.log(label(`[${m.type}]`), m.content?.slice(0, 200));
        });
      } catch (err) {
        console.error(chalk.red(`Error: ${err.message}`));
      }
      rl.prompt();
      return;
    }

    try {
      const { responses } = await client.chatWithTask(taskId, projectDir, input);
      for (const msg of responses) {
        if (msg.event === 'ai_response' || msg.event === 'chat_message') {
          const content = msg.data?.content || msg.message || '';
          console.log(chalk.green(content));
        } else if (msg.event === 'error') {
          console.error(chalk.red(msg.message || msg.data?.error));
        } else if (msg.event === 'agent_msg') {
          process.stdout.write(chalk.blue(typeof msg.data === 'string' ? msg.data : ''));
        }
      }
    } catch (err) {
      console.error(chalk.red(`Error: ${err.message}`));
    }
    rl.prompt();
  });

  rl.on('close', () => {
    console.log(chalk.dim('\nChat ended.'));
    process.exit(0);
  });
}

async function cmdStatus(client, args) {
  if (args.length < 1) {
    console.error(chalk.red('Usage: codeactor status <task-id>'));
    process.exit(1);
  }
  const taskId = args[0];
  try {
    const s = await client.getTaskStatus(taskId);
    console.log(chalk.bold(`Task: ${s.task_id}`));
    console.log(`  Status:    ${s.status === 'running' ? chalk.yellow(s.status) : s.status === 'finished' ? chalk.green(s.status) : chalk.red(s.status)}`);
    console.log(`  Created:   ${s.created_at}`);
    console.log(`  Updated:   ${s.updated_at}`);
    if (s.progress) console.log(`  Progress:  ${s.progress}`);
    if (s.result) {
      console.log(chalk.dim('─'.repeat(40)));
      console.log(s.result);
    }
    if (s.error) console.log(chalk.red(`  Error:     ${s.error}`));
    if (s.memory) {
      console.log(`  Memory:    ${s.memory.size} / ${s.memory.max_size} messages`);
    }
  } catch (err) {
    console.error(chalk.red(`Error: ${err.message}`));
    process.exit(1);
  }
}

async function cmdCancel(client, args) {
  if (args.length < 1) {
    console.error(chalk.red('Usage: codeactor cancel <task-id>'));
    process.exit(1);
  }
  try {
    const result = await client.cancelTask(args[0]);
    console.log(chalk.yellow(result.message));
  } catch (err) {
    console.error(chalk.red(`Error: ${err.message}`));
    process.exit(1);
  }
}

async function cmdHistory(client) {
  try {
    const items = await client.getHistory();
    if (!items.length) {
      console.log(chalk.gray('No task history found.'));
      return;
    }
    for (const item of items) {
      console.log(`  ${chalk.cyan(item.task_id.slice(0, 8))}...  ${chalk.bold(item.title?.slice(0, 60) || '')}`);
      console.log(`          ${item.message_count} messages  |  ${item.updated_at}`);
    }
  } catch (err) {
    console.error(chalk.red(`Error: ${err.message}`));
    process.exit(1);
  }
}

async function cmdMemory(client, args) {
  if (args.length < 1) {
    console.error(chalk.red('Usage: codeactor memory <task-id>'));
    process.exit(1);
  }
  try {
    const mem = await client.getMemory(args[0]);
    console.log(chalk.bold(`Memory for ${args[0]} (${mem.size} / ${mem.max_size} messages)`));
    console.log(chalk.dim('─'.repeat(60)));
    for (const m of mem.messages || []) {
      const label = {
        system: chalk.gray('[system]'),
        human: chalk.cyan('[human]'),
        assistant: chalk.green('[assistant]'),
        tool: chalk.yellow('[tool]'),
      }[m.type] || chalk.dim(`[${m.type}]`);
      console.log(label, m.content);
      if (m.tool_calls?.length) {
        for (const tc of m.tool_calls) {
          console.log(chalk.yellow(`  [tool_call] ${tc.name || tc.function?.name || ''}`));
        }
      }
      console.log('');
    }
  } catch (err) {
    console.error(chalk.red(`Error: ${err.message}`));
    process.exit(1);
  }
}

async function cmdLoad(client, args) {
  if (args.length < 2) {
    console.error(chalk.red('Usage: codeactor load <task-id> <project-dir>'));
    process.exit(1);
  }
  try {
    const result = await client.loadTask(args[0], args[1]);
    console.log(chalk.green(result.message));
  } catch (err) {
    console.error(chalk.red(`Error: ${err.message}`));
    process.exit(1);
  }
}

// ── main ─────────────────────────────────────────────────────

async function main() {
  const { opts, args } = parseArgs(process.argv.slice(2));

  if (args.length === 0) {
    usage();
    process.exit(0);
  }

  const command = args.shift();
  const client = new CodeActorClient(opts.host, opts.port);

  switch (command) {
    case 'run':      return cmdRun(client, args);
    case 'chat':     return cmdChat(client, args);
    case 'status':   return cmdStatus(client, args);
    case 'cancel':   return cmdCancel(client, args);
    case 'history':  return cmdHistory(client);
    case 'memory':   return cmdMemory(client, args);
    case 'load':     return cmdLoad(client, args);
    default:
      console.error(chalk.red(`Unknown command: ${command}`));
      usage();
      process.exit(1);
  }
}

main();
