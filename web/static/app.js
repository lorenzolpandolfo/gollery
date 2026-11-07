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

  function currentPath() {
    return $("path-input").value;
  }

  /* ---------------- Tema ---------------- */

  $("theme-toggle").addEventListener("click", function () {
    var next = root.dataset.theme === "dark" ? "light" : "dark";
    root.dataset.theme = next;
    safeSet("gollery-theme", next);
  });

  /* ---------------- Grade / lista ---------------- */

  var viewButtons = document.querySelectorAll(".view-toggle button");

  function syncViewButtons() {
    Array.prototype.forEach.call(viewButtons, function (button) {
      button.setAttribute(
        "aria-pressed",
        String(button.dataset.view === root.dataset.view)
      );
    });
  }

  Array.prototype.forEach.call(viewButtons, function (button) {
    button.addEventListener("click", function () {
      root.dataset.view = button.dataset.view;
      safeSet("gollery-view", button.dataset.view);
      syncViewButtons();
    });
  });

  syncViewButtons();

  /* ---------------- Ordenação e pesquisa ---------------- */

  var sortForm = $("sort-form");
  var sortSelect = $("sort-select");
  var orderInput = $("order-input");
  var orderToggle = $("order-toggle");
  var searchInput = $("search-input");
  var searchTimer = null;

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
    var by = sortSelect.value;
    var order = orderInput.value;

    safeSet("gollery-sort", JSON.stringify({ by: by, order: order }));

    // O servidor usa este cookie para já renderizar a página ordenada.
    try {
      document.cookie =
        "gollery_sort=" + by + "." + order + "; path=/; max-age=31536000; SameSite=Lax";
    } catch (e) {}
  }

  sortSelect.addEventListener("change", saveSort);

  orderToggle.addEventListener("click", function () {
    orderInput.value = orderInput.value === "asc" ? "desc" : "asc";
    syncOrderUI();
    saveSort();
    refreshList();
  });

  // Pesquisa enquanto digita (com debounce). O "change" do campo é bloqueado
  // para não disparar uma segunda requisição ao sair do campo.
  searchInput.addEventListener("input", function () {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(refreshList, 300);
  });

  searchInput.addEventListener("change", function (e) {
    e.stopPropagation();
  });

  sortForm.addEventListener("submit", function (e) {
    e.preventDefault();
    clearTimeout(searchTimer);
    refreshList();
  });

  syncOrderUI();

  document.addEventListener("DOMContentLoaded", function () {
    // Restaura a ordenação salva caso o servidor tenha renderizado outra.
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
    saveSort();

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
  var folderDialog = $("folder-dialog");

  closeOnBackdrop(confirmDialog);
  closeOnBackdrop(doneDialog);
  closeOnBackdrop(folderDialog);

  function confirmDeletion(name, isFolder) {
    return new Promise(function (resolve) {
      $("confirm-title").textContent = isFolder
        ? "Excluir pasta?"
        : "Excluir arquivo?";

      var subject = name ? "“" + name + "”" : isFolder ? "Essa pasta" : "Esse arquivo";

      $("confirm-text").textContent = isFolder
        ? subject + " e tudo o que está dentro dela serão excluídos permanentemente. Essa ação não pode ser desfeita."
        : subject + " será excluído permanentemente. Essa ação não pode ser desfeita.";

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

    var elt = e.detail.elt;
    var name = elt && elt.dataset.name;
    var isFolder = !!elt && elt.dataset.kind === "folder";

    confirmDeletion(name, isFolder).then(function (confirmed) {
      if (confirmed) e.detail.issueRequest(true);
    });
  });

  /* ---------------- Nova pasta ---------------- */

  var folderForm = $("folder-form");
  var folderName = $("folder-name");
  var folderError = $("folder-error");

  function showFolderError(message) {
    folderError.textContent = message;
    folderError.hidden = false;
  }

  $("new-folder").addEventListener("click", function () {
    folderName.value = "";
    folderError.hidden = true;
    folderDialog.showModal();
    folderName.focus();
  });

  $("folder-cancel").addEventListener("click", function () {
    folderDialog.close();
  });

  folderForm.addEventListener("submit", async function (e) {
    e.preventDefault();

    var name = folderName.value.trim();

    if (!name) {
      showFolderError("Digite um nome para a pasta.");
      return;
    }

    try {
      var response = await fetch("/folders", {
        method: "POST",
        body: new URLSearchParams({ path: currentPath(), name: name }),
      });

      if (response.ok) {
        folderDialog.close();
        refreshList();
        return;
      }

      if (response.status === 409) {
        showFolderError("Já existe uma pasta com esse nome aqui.");
      } else if (response.status === 400) {
        showFolderError("Nome inválido. Não use / ou \\ e não comece com ponto.");
      } else if (response.status === 507) {
        showFolderError("Sem espaço suficiente no servidor.");
      } else {
        showFolderError("Não foi possível criar a pasta.");
      }
    } catch (err) {
      showFolderError("Falha de conexão.");
    }
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
    if (xhr.status === 404) return "a pasta de destino não existe mais";
    var text = (xhr.responseText || "").trim();
    return text || "erro " + xhr.status;
  }

  function sendFile(file, destination, onProgress) {
    return new Promise(function (resolve, reject) {
      var xhr = new XMLHttpRequest();
      currentXhr = xhr;

      xhr.open("POST", "/upload?path=" + encodeURIComponent(destination));

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

    // A pasta de destino é fixada no início, mesmo que a pesquisa mude depois.
    var destination = currentPath();

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
        await sendFile(file, destination, function (loaded) {
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

  /* ---------------- Arrastar e soltar ---------------- */

  var dropOverlay = $("drop-overlay");
  var dragDepth = 0;

  function hasFiles(e) {
    return (
      !!e.dataTransfer &&
      Array.prototype.indexOf.call(e.dataTransfer.types || [], "Files") !== -1
    );
  }

  function droppedFiles(dataTransfer) {
    var files = [];
    var items = dataTransfer.items;

    if (items && items.length) {
      for (var i = 0; i < items.length; i++) {
        var item = items[i];
        if (item.kind !== "file") continue;

        // Pastas arrastadas são ignoradas; só arquivos são enviados.
        var entry = item.webkitGetAsEntry && item.webkitGetAsEntry();
        if (entry && !entry.isFile) continue;

        var file = item.getAsFile();
        if (file) files.push(file);
      }
      return files;
    }

    return Array.prototype.slice.call(dataTransfer.files);
  }

  window.addEventListener("dragenter", function (e) {
    if (!hasFiles(e) || uploading) return;
    e.preventDefault();
    dragDepth++;
    dropOverlay.hidden = false;
  });

  window.addEventListener("dragover", function (e) {
    if (hasFiles(e)) e.preventDefault();
  });

  window.addEventListener("dragleave", function (e) {
    if (!hasFiles(e)) return;
    dragDepth = Math.max(0, dragDepth - 1);
    if (dragDepth === 0) dropOverlay.hidden = true;
  });

  window.addEventListener("drop", function (e) {
    if (!hasFiles(e)) return;

    e.preventDefault();
    dragDepth = 0;
    dropOverlay.hidden = true;

    if (uploading) return;

    var files = droppedFiles(e.dataTransfer);
    if (files.length > 0) startUpload(files);
  });
})();
