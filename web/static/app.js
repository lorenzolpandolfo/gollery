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

  /* ---------------- Seleção múltipla / Ações em lote ---------------- */

  var bulkToolbar = $("bulk-toolbar");
  var bulkCount = $("bulk-count");
  var bulkDownloadBtn = $("bulk-download");
  var bulkDeleteBtn = $("bulk-delete");
  var bulkCancelBtn = $("bulk-cancel");
  var fileList = $("file-list");

  var selectedItems = new Set();

  function getItemKey(itemEl) {
    var path = itemEl.dataset.path;
    var kind = itemEl.dataset.kind;
    return kind + ":" + path;
  }

  function updateBulkToolbar() {
    var count = selectedItems.size;
    if (count > 0) {
      bulkCount.textContent = count + " " + plural(count, "item selecionado", "itens selecionados");
      bulkToolbar.hidden = false;
    } else {
      bulkToolbar.hidden = true;
    }
  }

  function updateItemSelection(itemEl, selected) {
    var card = itemEl.closest(".file-card");
    if (card) {
      card.classList.toggle("selected", selected);
    }
  }

  function selectItem(itemEl) {
    var key = getItemKey(itemEl);
    if (!selectedItems.has(key)) {
      selectedItems.add(key);
      itemEl.checked = true;
      updateItemSelection(itemEl, true);
    }
  }

  function deselectItem(itemEl) {
    var key = getItemKey(itemEl);
    if (selectedItems.has(key)) {
      selectedItems.delete(key);
      itemEl.checked = false;
      updateItemSelection(itemEl, false);
    }
  }

  function toggleItem(itemEl) {
    if (itemEl.checked) {
      selectItem(itemEl);
    } else {
      deselectItem(itemEl);
    }
    updateBulkToolbar();
  }

  function clearSelection() {
    selectedItems.forEach(function (key) {
      var parts = key.split(":");
      var kind = parts[0];
      var path = parts.slice(1).join(":");
      var checkbox = document.querySelector('.select-item[data-kind="' + kind + '"][data-path="' + path + '"]');
      if (checkbox) {
        checkbox.checked = false;
        updateItemSelection(checkbox, false);
      }
    });
    selectedItems.clear();
    updateBulkToolbar();
  }

  function getSelectedItems() {
    var items = [];
    selectedItems.forEach(function (key) {
      var parts = key.split(":");
      var kind = parts[0];
      var path = parts.slice(1).join(":");
      items.push({ kind: kind, path: path });
    });
    return items;
  }

  // Delegação de eventos para checkboxes
  fileList.addEventListener("change", function (e) {
    if (e.target.matches(".select-item")) {
      toggleItem(e.target);
    }
  });

  // Seleção via clique no card (Shift+click para seleção em intervalo)
  var lastSelectedCard = null;
  fileList.addEventListener("click", function (e) {
    var card = e.target.closest(".file-card");
    if (!card) return;

    if (longPressedCard === card) {
      e.preventDefault();
      longPressedCard = null;
      clearTimeout(longPressClickTimer);
      return;
    }

    var checkbox = card.querySelector(".select-item");
    if (!checkbox) return;

    if (e.target === checkbox || e.target.closest("button") || e.target.closest("label")) return;

    var clickedLink = e.target.closest("a");
    var mobileSelection = window.matchMedia("(max-width: 768px)").matches && selectedItems.size > 0;
    if (clickedLink && !mobileSelection) return;
    if (clickedLink) e.preventDefault();

    if (e.shiftKey && lastSelectedCard) {
      // Seleção em intervalo
      var cards = Array.prototype.slice.call(fileList.querySelectorAll(".file-card"));
      var start = cards.indexOf(lastSelectedCard);
      var end = cards.indexOf(card);
      if (start > end) { var tmp = start; start = end; end = tmp; }
      for (var i = start; i <= end; i++) {
        var cb = cards[i].querySelector(".select-item");
        if (cb && !cb.checked) selectItem(cb);
      }
      updateBulkToolbar();
    } else {
      // Toggle simples
      checkbox.checked = !checkbox.checked;
      toggleItem(checkbox);
    }
    lastSelectedCard = card;
  });

  bulkCancelBtn.addEventListener("click", clearSelection);

  // Download em lote
  bulkDownloadBtn.addEventListener("click", async function () {
    var items = getSelectedItems();
    if (items.length === 0) return;

    if (items.length === 1) {
      // Um único item: usa o link direto
      var item = items[0];
      var url = "/media/" + encodeURIComponent(item.path) + "?download=1";
      window.open(url, "_blank");
      return;
    }

    // Múltiplos itens: solicita ZIP ao servidor
    bulkDownloadBtn.disabled = true;
    bulkDownloadBtn.textContent = " Preparando...";

    try {
      var response = await fetch("/bulk-download", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ items: items }),
      });

      if (response.ok) {
        var blob = await response.blob();
        var url = URL.createObjectURL(blob);
        var a = document.createElement("a");
        a.href = url;
        a.download = "gollery-selecao-" + Date.now() + ".zip";
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
      } else {
        var err = await response.text();
        alert("Erro ao criar ZIP: " + (err || response.statusText));
      }
    } catch (err) {
      alert("Falha de conexão: " + err.message);
    } finally {
      bulkDownloadBtn.disabled = false;
      bulkDownloadBtn.innerHTML = '<i class="icon-download"></i> Baixar';
    }
  });

  // Exclusão em lote
  bulkDeleteBtn.addEventListener("click", async function () {
    var items = getSelectedItems();
    if (items.length === 0) return;

    var confirmMsg = items.length === 1
      ? (items[0].kind === "folder" ? "Excluir esta pasta e todo o seu conteúdo?" : "Excluir este arquivo?")
      : "Excluir " + items.length + " itens selecionados?";

    if (!confirm(confirmMsg + " Esta ação não pode ser desfeita.")) return;

    bulkDeleteBtn.disabled = true;
    bulkDeleteBtn.textContent = " Excluindo...";

    try {
      var response = await fetch("/bulk-delete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ items: items }),
      });

      if (response.ok) {
        clearSelection();
        refreshList();
      } else {
        var err = await response.text();
        alert("Erro ao excluir: " + (err || response.statusText));
      }
    } catch (err) {
      alert("Falha de conexão: " + err.message);
    } finally {
      bulkDeleteBtn.disabled = false;
      bulkDeleteBtn.innerHTML = '<i class="icon-trash"></i> Excluir';
    }
  });

  // Limpa seleção quando a lista é atualizada via HTMX
  document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e.detail.target.id === "file-list") {
      clearSelection();
    }
  });

  // Limpa seleção ao navegar (mudança de pasta/pesquisa)
  sortForm.addEventListener("htmx:beforeRequest", function () {
    clearSelection();
  });

  /* ---------------- Mover itens (drag & drop) ---------------- */

  var moveDialog = $("move-dialog");
  var dragSourceCards = [];
  var dragOverFolder = null;
  var breadcrumb = document.querySelector(".breadcrumb");
  var longPressTimer = null;
  var longPressThreshold = 500; // ms
  var isLongPress = false;
  var longPressedCard = null;
  var longPressClickTimer = null;

  closeOnBackdrop(moveDialog);

  function isInvalidMoveTarget(items, targetPath) {
    return items.some(function (item) {
      return item.kind === "folder" &&
        (item.path === targetPath || targetPath.startsWith(item.path + "/"));
    });
  }

  function confirmMove(itemCount, targetFolderName) {
    return new Promise(function (resolve) {
      var title = itemCount === 1 ? "Mover item?" : "Mover " + itemCount + " itens?";
      var text = "Mover para a pasta “" + targetFolderName + "”?";

      $("move-title").textContent = title;
      $("move-text").textContent = text;

      moveDialog.returnValue = "";
      moveDialog.addEventListener(
        "close",
        function () {
          resolve(moveDialog.returnValue === "confirm");
        },
        { once: true }
      );
      moveDialog.showModal();
    });
  }

  // Drag start - inicia arrasto de itens selecionados ou do item atual
  fileList.addEventListener("dragstart", function (e) {
    var card = e.target.closest(".file-card");
    if (!card) return;

    // Não permite arrastar se está arrastando arquivos do sistema (upload)
    if (uploading) {
      e.preventDefault();
      return;
    }

    var checkbox = card.querySelector(".select-item");
    if (!checkbox) return;

    // Se tem itens selecionados e o card atual não está selecionado, usa a seleção
    // Se o card atual está selecionado, usa a seleção
    // Se não tem nada selecionado, seleciona apenas este card
    if (selectedItems.size === 0 || (selectedItems.size > 0 && checkbox.checked)) {
      // Usa itens selecionados
      dragSourceCards = getSelectedItems();
    } else {
      // Seleciona apenas este item para o drag
      dragSourceCards = [{ kind: checkbox.dataset.kind, path: checkbox.dataset.path }];
    }

    if (dragSourceCards.length === 0) {
      e.preventDefault();
      return;
    }

    // Define dados do drag
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", JSON.stringify(dragSourceCards));

    // Adiciona classe visual
    card.classList.add("dragging");
    dragSourceCards.forEach(function (item) {
      var selCard = document.querySelector('.select-item[data-kind="' + item.kind + '"][data-path="' + item.path + '"]');
      if (selCard) selCard.closest(".file-card").classList.add("dragging");
    });
  });

  fileList.addEventListener("dragend", function (e) {
    // Remove classes visuais
    document.querySelectorAll(".file-card.dragging").forEach(function (c) {
      c.classList.remove("dragging");
    });
    document.querySelectorAll(".file-card.drag-over").forEach(function (c) {
      c.classList.remove("drag-over");
    });
    breadcrumb.querySelectorAll(".drag-over").forEach(function (c) {
      c.classList.remove("drag-over");
    });
    dragSourceCards = [];
    dragOverFolder = null;
  });

  // Drag over - apenas pastas podem receber drop
  fileList.addEventListener("dragover", function (e) {
    var folderCard = e.target.closest(".folder-card");
    if (!folderCard) return;

    var checkbox = folderCard.querySelector(".select-item");
    if (!checkbox || checkbox.dataset.kind !== "folder") return;

    // Não permite soltar na própria pasta ou em subpastas dos itens arrastados
    var targetPath = checkbox.dataset.path;
    if (isInvalidMoveTarget(dragSourceCards, targetPath)) {
      e.dataTransfer.dropEffect = "none";
      return;
    }

    e.preventDefault();
    e.dataTransfer.dropEffect = "move";

    // Highlight visual
    if (dragOverFolder !== folderCard) {
      document.querySelectorAll(".file-card.drag-over").forEach(function (c) {
        c.classList.remove("drag-over");
      });
      folderCard.classList.add("drag-over");
      dragOverFolder = folderCard;
    }
  });

  fileList.addEventListener("dragleave", function (e) {
    var folderCard = e.target.closest(".folder-card");
    if (folderCard && !folderCard.contains(e.relatedTarget)) {
      folderCard.classList.remove("drag-over");
      if (dragOverFolder === folderCard) dragOverFolder = null;
    }
  });

  // Drop - move itens para a pasta
  fileList.addEventListener("drop", function (e) {
    var folderCard = e.target.closest(".folder-card");
    if (!folderCard) return;

    var checkbox = folderCard.querySelector(".select-item");
    if (!checkbox || checkbox.dataset.kind !== "folder") return;

    e.preventDefault();

    var targetPath = checkbox.dataset.path;
    var targetName = checkbox.dataset.name;
    var itemsToMove = dragSourceCards.slice();

    // Valida se não está tentando mover para si mesmo ou descendentes
    if (isInvalidMoveTarget(itemsToMove, targetPath)) {
      folderCard.classList.remove("drag-over");
      dragOverFolder = null;
      return;
    }

    // Confirma a ação
    confirmMove(itemsToMove.length, targetName).then(function (confirmed) {
      if (!confirmed) {
        folderCard.classList.remove("drag-over");
        dragOverFolder = null;
        return;
      }

      // Executa o move
      moveItems(itemsToMove, targetPath).then(function (success) {
        folderCard.classList.remove("drag-over");
        dragOverFolder = null;
        if (success) {
          clearSelection();
          refreshList();
        }
      });
    });
  });

  breadcrumb.addEventListener("dragover", function (e) {
    var target = e.target.closest("[data-drop-path]");
    if (!target || dragSourceCards.length === 0) return;

    var targetPath = target.dataset.dropPath;
    if (isInvalidMoveTarget(dragSourceCards, targetPath)) {
      e.dataTransfer.dropEffect = "none";
      return;
    }

    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    target.classList.add("drag-over");
  });

  breadcrumb.addEventListener("dragleave", function (e) {
    var target = e.target.closest("[data-drop-path]");
    if (target && !target.contains(e.relatedTarget)) {
      target.classList.remove("drag-over");
    }
  });

  breadcrumb.addEventListener("drop", function (e) {
    var target = e.target.closest("[data-drop-path]");
    if (!target || dragSourceCards.length === 0) return;

    e.preventDefault();
    var itemsToMove = dragSourceCards.slice();
    var targetPath = target.dataset.dropPath;
    var targetName = target.textContent.trim();
    target.classList.remove("drag-over");

    if (isInvalidMoveTarget(itemsToMove, targetPath)) return;

    confirmMove(itemsToMove.length, targetName).then(function (confirmed) {
      if (!confirmed) return;

      moveItems(itemsToMove, targetPath).then(function (success) {
        if (success) {
          clearSelection();
          refreshList();
        }
      });
    });
  });

  async function moveItems(items, targetPath) {
    try {
      var response = await fetch("/bulk-move", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ items: items, targetPath: targetPath }),
      });

      if (response.ok) {
        return true;
      } else {
        var err = await response.text();
        alert("Erro ao mover: " + (err || response.statusText));
        return false;
      }
    } catch (err) {
      alert("Falha de conexão: " + err.message);
      return false;
    }
  }

  /* ---------------- Long press para seleção (mobile) ---------------- */

  fileList.addEventListener("touchstart", function (e) {
    var card = e.target.closest(".file-card");
    if (!card) return;

    var checkbox = card.querySelector(".select-item");
    if (!checkbox) return;

    // Se já está selecionado, não inicia long press
    if (checkbox.checked) return;

    // Não inicia long press se tocou em checkbox ou botão
    if (e.target.closest("button") || e.target.closest("label") || e.target === checkbox) return;

    isLongPress = false;
    longPressTimer = setTimeout(function () {
      isLongPress = true;
      checkbox.checked = true;
      toggleItem(checkbox);
      longPressedCard = card;
      clearTimeout(longPressClickTimer);
      longPressClickTimer = setTimeout(function () {
        longPressedCard = null;
      }, 400);
      // Feedback tátil
      if (navigator.vibrate) navigator.vibrate(50);
    }, longPressThreshold);
  });

  fileList.addEventListener("touchend", function (e) {
    if (longPressTimer) {
      clearTimeout(longPressTimer);
      longPressTimer = null;
    }

    // Se foi long press, impede o click normal
    if (isLongPress) {
      isLongPress = false;
      e.preventDefault();
    }
  });

  fileList.addEventListener("touchmove", function (e) {
    // Cancela long press se moveu o dedo
    if (longPressTimer) {
      clearTimeout(longPressTimer);
      longPressTimer = null;
    }
  });

  /* ---------------- Touch drag para mobile (arrastar para mover) ---------------- */

  var touchDragSource = null;
  var touchDragStartX = 0;
  var touchDragStartY = 0;
  var touchDragThreshold = 10; // pixels

  fileList.addEventListener("touchstart", function (e) {
    if (isLongPress) return; // Não inicia drag se foi long press

    var card = e.target.closest(".file-card");
    if (!card) return;

    var checkbox = card.querySelector(".select-item");
    if (!checkbox) return;

    // Não inicia drag se tocou em botão ou checkbox
    if (e.target.closest("button") || e.target.closest("label") || e.target === checkbox) return;

    touchDragSource = {
      card: card,
      checkbox: checkbox,
      startX: e.touches[0].clientX,
      startY: e.touches[0].clientY,
    };
  }, { passive: true });

  fileList.addEventListener("touchmove", function (e) {
    if (!touchDragSource) return;

    var deltaX = e.touches[0].clientX - touchDragSource.startX;
    var deltaY = e.touches[0].clientY - touchDragSource.startY;
    var distance = Math.sqrt(deltaX * deltaX + deltaY * deltaY);

    if (distance > touchDragThreshold) {
      // Inicia drag visual
      touchDragSource.card.classList.add("dragging");
      e.preventDefault(); // Impede scroll
    }
  }, { passive: false });

  fileList.addEventListener("touchend", function (e) {
    if (!touchDragSource) return;

    var card = touchDragSource.card;
    var checkbox = touchDragSource.checkbox;

    card.classList.remove("dragging");

    // Verifica se soltou sobre uma pasta ou breadcrumb
    var touch = e.changedTouches[0];
    var targetEl = document.elementFromPoint(touch.clientX, touch.clientY);
    var folderCard = targetEl && targetEl.closest(".folder-card");
    var crumbTarget = targetEl && targetEl.closest("[data-drop-path]");

    if (folderCard || crumbTarget) {
      var folderCheckbox = folderCard && folderCard.querySelector(".select-item");
      if (crumbTarget || (folderCheckbox && folderCheckbox.dataset.kind === "folder")) {
        var targetPath = crumbTarget ? crumbTarget.dataset.dropPath : folderCheckbox.dataset.path;
        var targetName = crumbTarget ? crumbTarget.textContent.trim() : folderCheckbox.dataset.name;

        // Determina itens a mover
        var itemsToMove = selectedItems.size > 0 && checkbox.checked
          ? getSelectedItems()
          : [{ kind: checkbox.dataset.kind, path: checkbox.dataset.path }];

        if (!isInvalidMoveTarget(itemsToMove, targetPath)) {
          confirmMove(itemsToMove.length, targetName).then(function (confirmed) {
            if (confirmed) {
              moveItems(itemsToMove, targetPath).then(function (success) {
                if (success) {
                  clearSelection();
                  refreshList();
                }
              });
            }
          });
        }
      }
    }

    touchDragSource = null;
  });

  // Cleanup touch drag no cancelamento
  window.addEventListener("touchcancel", function () {
    if (touchDragSource) {
      touchDragSource.card.classList.remove("dragging");
      touchDragSource = null;
    }
    if (longPressTimer) {
      clearTimeout(longPressTimer);
      longPressTimer = null;
    }
  });
})();
