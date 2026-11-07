(function () {
  "use strict";

  var root = document.documentElement;

  function $(id) {
    return document.getElementById(id);
  }

  function safeGet(key) {
    try {
      return localStorage.getItem(key);
    } catch (e) {
      return null;
    }
  }

  function safeSet(key, value) {
    try {
      localStorage.setItem(key, value);
    } catch (e) {}
  }

  function formatBytes(n) {
    if (n < 1024) return n + " B";
    var units = ["KB", "MB", "GB", "TB"];
    var value = n / 1024;
    var i = 0;
    while (value >= 1024 && i < units.length - 1) {
      value /= 1024;
      i++;
    }
    return value.toFixed(1).replace(".", ",") + " " + units[i];
  }

  function plural(n, one, many) {
    return n === 1 ? one : many;
  }

  /* ---------------- Tema ---------------- */

  $("theme-toggle").addEventListener("click", function () {
    var next = root.dataset.theme === "dark" ? "light" : "dark";
    root.dataset.theme = next;
    safeSet("gollery-theme", next);
  });

  /* ---------------- Ordenação ---------------- */

  var sortForm = $("sort-form");
  var sortSelect = $("sort-select");
  var orderInput = $("order-input");
  var orderToggle = $("order-toggle");

  function refreshList() {
    if (window.htmx) htmx.trigger(sortForm, "refresh");
  }

  function syncOrderUI() {
    orderToggle.dataset.order = orderInput.value;
    orderToggle.title =
      orderInput.value === "asc"
        ? "Ordem crescente (clique para inverter)"
        : "Ordem decrescente (clique para inverter)";
  }

  function saveSort() {
    safeSet(
      "gollery-sort",
      JSON.stringify({ by: sortSelect.value, order: orderInput.value })
    );
  }

  sortSelect.addEventListener("change", saveSort);

  orderToggle.addEventListener("click", function () {
    orderInput.value = orderInput.value === "asc" ? "desc" : "asc";
    syncOrderUI();
    saveSort();
    refreshList();
  });

  syncOrderUI();

  document.addEventListener("DOMContentLoaded", function () {
    // Restaura a ordenação salva (o servidor renderiza com o padrão).
    var saved;
    try {
      saved = JSON.parse(safeGet("gollery-sort"));
    } catch (e) {
      saved = null;
    }

    if (!saved) return;

    var validBy = ["date", "name", "type"].indexOf(saved.by) !== -1;
    var validOrder = saved.order === "asc" || saved.order === "desc";

    if (!validBy || !validOrder) return;
    if (saved.by === sortSelect.value && saved.order === orderInput.value) return;

    sortSelect.value = saved.by;
    orderInput.value = saved.order;
    syncOrderUI();

    // Dá tempo ao HTMX de processar a página antes de disparar a requisição.
    setTimeout(refreshList, 0);
  });

  /* ---------------- Modais ---------------- */

  function closeOnBackdrop(dialog) {
    dialog.addEventListener("click", function (e) {
      if (e.target === dialog) dialog.close("cancel");
    });
  }

  var confirmDialog = $("confirm-dialog");
  var doneDialog = $("upload-done");

  closeOnBackdrop(confirmDialog);
  closeOnBackdrop(doneDialog);

  function confirmDeletion(name) {
    return new Promise(function (resolve) {
      $("confirm-text").textContent = name
        ? "“" + name + "” será excluído permanentemente. Essa ação não pode ser desfeita."
        : "Esse arquivo será excluído permanentemente. Essa ação não pode ser desfeita.";

      confirmDialog.returnValue = "";
      confirmDialog.addEventListener(
        "close",
        function () {
          resolve(confirmDialog.returnValue === "confirm");
        },
        { once: true }
      );
      confirmDialog.showModal();
    });
  }

  // Intercepta o hx-confirm do HTMX e usa o modal em vez do confirm() nativo.
  document.body.addEventListener("htmx:confirm", function (e) {
    if (!e.detail.question) return;

    e.preventDefault();

    var name = e.detail.elt && e.detail.elt.dataset.name;

    confirmDeletion(name).then(function (confirmed) {
      if (confirmed) e.detail.issueRequest(true);
    });
  });

  /* ---------------- Upload ---------------- */

  var fileInput = $("file-input");

  var panel = $("upload-panel");
  var uploadTitle = $("upload-title");
  var uploadFilename = $("upload-filename");
  var uploadFilePct = $("upload-file-pct");
  var uploadFileBar = $("upload-file-bar");
  var uploadTotalText = $("upload-total-text");
  var uploadTotalPct = $("upload-total-pct");
  var uploadTotalBar = $("upload-total-bar");

  var uploading = false;
  var cancelled = false;
  var currentXhr = null;

  function percent(done, total) {
    if (total <= 0) return 100;
    return Math.max(0, Math.min(100, Math.round((done / total) * 100)));
  }

  function setBar(bar, pctLabel, done, total) {
    var pct = percent(done, total);
    bar.style.width = pct + "%";
    bar.parentNode.setAttribute("aria-valuenow", String(pct));
    pctLabel.textContent = pct + "%";
  }

  function errorMessage(xhr) {
    if (xhr.status === 507) return "sem espaço suficiente no servidor";
    var text = (xhr.responseText || "").trim();
    return text || "erro " + xhr.status;
  }

  function sendFile(file, onProgress) {
    return new Promise(function (resolve, reject) {
      var xhr = new XMLHttpRequest();
      currentXhr = xhr;

      xhr.open("POST", "/upload");

      xhr.upload.onprogress = function (e) {
        if (!e.lengthComputable || e.total === 0) return;
        // e.loaded inclui o overhead do multipart; normaliza para o tamanho do arquivo.
        onProgress((e.loaded / e.total) * file.size);
      };

      xhr.onload = function () {
        currentXhr = null;
        if (xhr.status >= 200 && xhr.status < 300) {
          onProgress(file.size);
          resolve();
        } else {
          reject(new Error(errorMessage(xhr)));
        }
      };

      xhr.onerror = function () {
        currentXhr = null;
        reject(new Error("falha de conexão"));
      };

      xhr.onabort = function () {
        currentXhr = null;
        reject({ cancelled: true });
      };

      var data = new FormData();
      data.append("Files", file, file.name);
      xhr.send(data);
    });
  }

  function showDone(result) {
    var title;
    var text;

    if (result.cancelled) {
      title = "Upload cancelado";
      text =
        result.ok + " de " + result.total + " " + plural(result.total, "arquivo enviado", "arquivos enviados") + " antes do cancelamento.";
    } else if (result.errors.length === 0) {
      title = "Upload concluído";
      text =
        result.ok + " " + plural(result.ok, "arquivo enviado", "arquivos enviados") + " com sucesso.";
    } else if (result.ok === 0) {
      title = "Falha no upload";
      text = "Nenhum arquivo foi enviado.";
    } else {
      title = "Upload concluído com erros";
      text =
        result.ok + " de " + result.total + " " + plural(result.total, "arquivo enviado", "arquivos enviados") + ".";
    }

    $("done-title").textContent = title;
    $("done-text").textContent = text;

    var list = $("done-errors");
    list.textContent = "";

    result.errors.forEach(function (item) {
      var li = document.createElement("li");
      var name = document.createElement("strong");
      name.textContent = item.name;
      li.appendChild(name);
      li.appendChild(document.createTextNode(" — " + item.reason));
      list.appendChild(li);
    });

    list.hidden = result.errors.length === 0;

    doneDialog.showModal();
  }

  async function startUpload(files) {
    uploading = true;
    cancelled = false;
    fileInput.disabled = true;

    var total = files.reduce(function (sum, f) {
      return sum + f.size;
    }, 0);

    var completedBytes = 0;
    var ok = 0;
    var errors = [];

    panel.hidden = false;
    setBar(uploadTotalBar, uploadTotalPct, 0, total);
    uploadTotalText.textContent = "0 B de " + formatBytes(total);

    function updateTotal(loaded) {
      var done = completedBytes + loaded;
      setBar(uploadTotalBar, uploadTotalPct, done, total);
      uploadTotalText.textContent = formatBytes(done) + " de " + formatBytes(total);
    }

    for (var i = 0; i < files.length && !cancelled; i++) {
      var file = files[i];

      uploadTitle.textContent = "Enviando " + (i + 1) + " de " + files.length;
      uploadFilename.textContent = file.name;
      uploadFilename.title = file.name;
      setBar(uploadFileBar, uploadFilePct, 0, file.size);

      try {
        await sendFile(file, function (loaded) {
          setBar(uploadFileBar, uploadFilePct, loaded, file.size);
          updateTotal(loaded);
        });
        ok++;
      } catch (err) {
        if (err && err.cancelled) break;
        errors.push({ name: file.name, reason: err.message });
      }

      completedBytes += file.size;
      updateTotal(0);
    }

    var wasCancelled = cancelled;

    panel.hidden = true;
    fileInput.disabled = false;
    uploading = false;
    cancelled = false;

    if (ok > 0) refreshList();

    showDone({
      ok: ok,
      total: files.length,
      errors: errors,
      cancelled: wasCancelled,
    });
  }

  fileInput.addEventListener("change", function () {
    var files = Array.prototype.slice.call(fileInput.files);
    fileInput.value = "";

    if (files.length > 0 && !uploading) startUpload(files);
  });

  $("upload-cancel").addEventListener("click", function () {
    cancelled = true;
    if (currentXhr) currentXhr.abort();
  });

  window.addEventListener("beforeunload", function (e) {
    if (uploading) {
      e.preventDefault();
      e.returnValue = "";
    }
  });
})();
