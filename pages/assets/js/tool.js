/* 取码助手：在浏览器本地完成 HMAC-SHA256 计算并生成可直接复制的命令。
   密钥与手机号只存在于当前页面内存，不发送到任何服务器（本文件无任何网络请求）。 */
(function () {
  'use strict';

  /* ---------- 公共：toast 与复制 ---------- */

  var toastEl = document.querySelector('.toast');

  function toast(msg) {
    if (!toastEl) return;
    toastEl.textContent = msg;
    toastEl.classList.add('show');
    clearTimeout(toastEl._timer);
    toastEl._timer = setTimeout(function () {
      toastEl.classList.remove('show');
    }, 1800);
  }

  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try {
      ok = document.execCommand('copy');
    } catch (e) {
      ok = false;
    }
    document.body.removeChild(ta);
    return ok;
  }

  function copyText(text, okMsg) {
    function done() { toast(okMsg || '已复制'); }
    function fail() { toast(fallbackCopy(text) ? (okMsg || '已复制') : '复制失败，请手动选择文本'); }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, fail);
    } else {
      fail();
    }
  }

  // [data-copy-target] 按钮统一走事件委托
  document.addEventListener('click', function (ev) {
    var btn = ev.target.closest('[data-copy-target]');
    if (!btn) return;
    var src = document.getElementById(btn.getAttribute('data-copy-target'));
    if (!src) return;
    copyText(src.textContent, btn.getAttribute('data-ok-msg') || '已复制');
  });

  /* ---------- HMAC-SHA256（Web Crypto，输出小写十六进制） ---------- */

  function hmacSha256Hex(secret, message) {
    var enc = new TextEncoder();
    return crypto.subtle.importKey(
      'raw',
      enc.encode(secret),
      { name: 'HMAC', hash: 'SHA-256' },
      false,
      ['sign']
    ).then(function (key) {
      return crypto.subtle.sign('HMAC', key, enc.encode(message));
    }).then(function (sig) {
      var bytes = new Uint8Array(sig);
      var hex = '';
      for (var i = 0; i < bytes.length; i++) {
        hex += (bytes[i] < 16 ? '0' : '') + bytes[i].toString(16);
      }
      return hex;
    });
  }

  /* ---------- shell / JSON 组装 ---------- */

  // 单引号包裹的 shell 安全转义：' -> '\''
  function shellQuote(s) {
    return "'" + String(s).replace(/'/g, "'\\''") + "'";
  }

  // 生成 curl 命令（多行带反斜杠续行）
  function buildWebhookCurl(server, webhookSecret, payload) {
    var url = server.replace(/\/+$/, '') + '/api/v1/webhook/sms/' + encodeURIComponent(webhookSecret);
    return 'curl -s -X POST ' + shellQuote(url) + ' \\\n' +
      "  -H 'Content-Type: application/json' \\\n" +
      '  -d ' + shellQuote(JSON.stringify(payload));
  }

  function buildOtpCurl(server, token) {
    var url = server.replace(/\/+$/, '') + '/api/v1/otp';
    return 'curl -s -X POST ' + shellQuote(url) + ' \\\n' +
      '  -d ' + shellQuote(JSON.stringify({ token: token }));
  }

  // 与 README 一致的 python3 对照命令，用于交叉验证 HMAC 结果
  function buildPythonCheck(secret, phone) {
    return "python3 -c \"import hmac,hashlib;print(hmac.new(b'" +
      secret.replace(/'/g, '') + "',b'" + phone.replace(/'/g, '') +
      "',hashlib.sha256).hexdigest())\"";
  }

  /* ---------- 取码助手表单 ---------- */

  function initOtpTool() {
    var root = document.getElementById('otpTool');
    if (!root) return; // 本页没有取码助手时直接退出（docs.html 等也会加载本脚本）

    var $ = function (id) { return document.getElementById(id); };
    var fServer = $('fServer'), fSecret = $('fSecret'), fPhone = $('fPhone');
    var fWebhook = $('fWebhook'), fSender = $('fSender'), fBody = $('fBody');
    var fResult = $('fResult');
    var fTokenOut = $('fTokenOut'), fWebhookOut = $('fWebhookOut'),
      fOtpOut = $('fOtpOut'), fPythonOut = $('fPythonOut');
    var computeBtn = $('fCompute');

    function showError(msg) {
      toast(msg);
    }

    async function compute() {
      var server = fServer.value.trim() || 'http://127.0.0.1:53340';
      var secret = fSecret.value.trim();
      var phone = fPhone.value.trim();
      var webhookSecret = fWebhook.value.trim();
      var sender = fSender.value.trim() || '+8613800000000';
      var body = fBody.value;

      if (!secret) { showError('请先填写 HMAC_SECRET'); fSecret.focus(); return; }
      if (!phone) { showError('请先填写收件人手机号'); fPhone.focus(); return; }
      if (!('crypto' in window) || !crypto.subtle) {
        showError('当前环境不支持 Web Crypto，请改用 HTTPS 访问本页');
        return;
      }

      var token;
      try {
        token = await hmacSha256Hex(secret, phone);
      } catch (err) {
        showError('HMAC 计算失败：' + err.message);
        return;
      }

      fTokenOut.textContent = token;
      fWebhookOut.textContent = webhookSecret
        ? buildWebhookCurl(server, webhookSecret, {
          provider: 'smsforward',
          sender: sender,
          recipient: phone,
          body: body
        })
        : '# 填写 WEBHOOK_SECRET 后生成模拟短信命令';
      fOtpOut.textContent = buildOtpCurl(server, token);
      fPythonOut.textContent = buildPythonCheck(secret, phone);

      fResult.classList.add('show');
    }

    if (computeBtn) {
      computeBtn.addEventListener('click', compute);
    }
    // 回车直接计算
    root.addEventListener('keydown', function (ev) {
      if (ev.key === 'Enter' && ev.target.tagName === 'INPUT') {
        ev.preventDefault();
        compute();
      }
    });
  }

  /* ---------- SMSForward 模板生成器（smsforward.html 使用） ---------- */

  function initForwardTool() {
    var root = document.getElementById('forwardTool');
    if (!root) return;

    var $ = function (id) { return document.getElementById(id); };
    var fMode = $('gMode'), fServer = $('gServer'), fSecret = $('gSecret'),
      fChannel = $('gChannel'), fToken = $('gToken'), fRecipient = $('gRecipient');
    var gResult = $('gResult'), gUrlOut = $('gUrlOut'), gTemplateOut = $('gTemplateOut');
    var fieldSecret = $('gFieldSecret'), fieldChannel = $('gFieldChannel'), fieldToken = $('gFieldToken');

    function mode() { return fMode.value; }

    function syncMode() {
      var isChannel = mode() === 'channel';
      fieldSecret.style.display = isChannel ? 'none' : '';
      fieldChannel.style.display = isChannel ? '' : 'none';
      fieldToken.style.display = isChannel ? '' : 'none';
    }

    async function generate() {
      var server = fServer.value.trim().replace(/\/+$/, '');
      var recipient = fRecipient.value.trim();
      var isChannel = mode() === 'channel';
      var url;

      if (!server) { toast('请先填写服务地址'); fServer.focus(); return; }

      if (isChannel) {
        var channel = fChannel.value.trim();
        var token = fToken.value.trim();
        if (!channel) { toast('请填写通道 ID（channel_id）'); fChannel.focus(); return; }
        if (!token) { toast('请填写该通道的 webhook_secret'); fToken.focus(); return; }
        url = server + '/api/v1/webhook/smsforward/' + encodeURIComponent(channel) + '/' + encodeURIComponent(token);
      } else {
        var secret = fSecret.value.trim();
        if (!secret) { toast('请填写 WEBHOOK_SECRET'); fSecret.focus(); return; }
        url = server + '/api/v1/webhook/sms/' + encodeURIComponent(secret);
      }

      gUrlOut.textContent = url;

      // SmsForwarder 消息模板：占位符在发送时由 App 替换（JSON 体内自动按 JSON 转义）。
      // recipient 没有对应占位符（{{DEVICE_NAME}} 是设备备注不是号码），需填本机 SIM 卡号码。
      // 通道模式额外携带 source 供服务端做来源白名单校验。
      var tpl = '{\n' +
        '  "provider": "smsforward",\n' +
        '  "sender": "{{FROM}}",\n' +
        '  "recipient": "' + (recipient || '本机SIM卡号码（必填，如 +8613900000000）') + '",\n' +
        '  "body": "{{SMS}}"';
      if (isChannel) {
        tpl += ',\n  "source": "{{DEVICE_NAME}}"';
      }
      tpl += '\n}';

      gTemplateOut.textContent = tpl;
      gResult.classList.add('show');
    }

    fMode.addEventListener('change', syncMode);
    var genBtn = $('gCompute');
    if (genBtn) genBtn.addEventListener('click', generate);
    root.addEventListener('keydown', function (ev) {
      if (ev.key === 'Enter' && ev.target.tagName === 'INPUT') {
        ev.preventDefault();
        generate();
      }
    });
    syncMode();
  }

  /* ---------- token 计算页（token.html 使用） ---------- */

  function initTokenTool() {
    var root = document.getElementById('tokenTool');
    if (!root) return;

    var tRecipient = document.getElementById('tRecipient');
    var tSecret = document.getElementById('tSecret');
    var tServer = document.getElementById('tServer');
    var tResult = document.getElementById('tResult');
    var tTokenOut = document.getElementById('tTokenOut');
    var tPrefixOut = document.getElementById('tPrefixOut');
    var tCurlOut = document.getElementById('tCurlOut');
    var tPythonOut = document.getElementById('tPythonOut');

    async function compute() {
      var recipient = tRecipient.value.trim();
      var secret = tSecret.value.trim();
      var server = tServer.value.trim().replace(/\/+$/, '');

      if (!recipient) { toast('请先填写收件人号码'); tRecipient.focus(); return; }
      if (!secret) { toast('请先填写 HMAC_SECRET'); tSecret.focus(); return; }
      if (!('crypto' in window) || !crypto.subtle) {
        toast('当前环境不支持 Web Crypto，请改用 HTTPS 或 localhost 访问本页');
        return;
      }

      var token;
      try {
        token = await hmacSha256Hex(secret, recipient);
      } catch (err) {
        toast('HMAC 计算失败：' + err.message);
        return;
      }

      tTokenOut.textContent = token;
      // 前 8 位与日志里的 recipient_hash 一致，作为服务端对账锚点
      tPrefixOut.textContent = token.slice(0, 8);
      tCurlOut.textContent = server ? buildOtpCurl(server, token) : '# 填写服务地址后生成';
      tPythonOut.textContent = buildPythonCheck(secret, recipient);

      tResult.classList.add('show');
    }

    var computeBtn = document.getElementById('tCompute');
    if (computeBtn) computeBtn.addEventListener('click', compute);
    root.addEventListener('keydown', function (ev) {
      if (ev.key === 'Enter' && ev.target.tagName === 'INPUT') {
        ev.preventDefault();
        compute();
      }
    });
  }

  function init() {
    initOtpTool();
    initForwardTool();
    initTokenTool();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
