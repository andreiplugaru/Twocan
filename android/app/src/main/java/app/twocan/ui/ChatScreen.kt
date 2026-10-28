package app.twocan.ui

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Info
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import app.twocan.Contact
import app.twocan.Json
import app.twocan.Message
import app.twocan.NodeEvent
import app.twocan.Twocan
import app.twocan.userMessage
import java.text.DateFormat
import java.util.Date
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/** The share of the conversation's width a bubble may take. */
private const val BUBBLE_WIDTH = 0.8f

/** How long "typing…" shows after the contact last said they type (chat.TypingFor in Go). */
private const val TYPING_FOR_MS = 5_000L

/** The reactions a message's menu offers. */
private val REACTIONS = listOf("👍", "❤️", "😂", "😮", "😢", "🙏")

/** What the next send does: a plain message, a reply, or an edit. */
sealed interface Compose {
    data object Plain : Compose
    data class Reply(val to: Message) : Compose
    data class Edit(val message: Message) : Compose
}

/** The conversation with one contact. */
class ChatViewModel(private val twocan: Twocan, val peer: String) : ViewModel() {
    private val _messages = MutableStateFlow<List<Message>>(emptyList())
    /** Oldest first. */
    val messages: StateFlow<List<Message>> = _messages.asStateFlow()
    private val _error = MutableStateFlow<String?>(null)
    /** Why the last action or load failed; null if it did not. */
    val error: StateFlow<String?> = _error.asStateFlow()
    private val _typing = MutableStateFlow(false)
    val typing: StateFlow<Boolean> = _typing.asStateFlow()
    private val _compose = MutableStateFlow<Compose>(Compose.Plain)
    val compose: StateFlow<Compose> = _compose.asStateFlow()
    private var typingJob: Job? = null

    init {
        viewModelScope.launch {
            // Subscribe before History runs (UNDISPATCHED: the collector is
            // subscribed by the time launch returns), so no message slips
            // between the two; add() skips one History already returned.
            launch(start = CoroutineStart.UNDISPATCHED) {
                twocan.events.collect { ev ->
                    when (ev) {
                        is NodeEvent.MessageArrived -> if (ev.message.peer == peer) {
                            add(ev.message)
                            setTyping(false)
                            markRead()
                        }
                        is NodeEvent.Delivered -> if (ev.peer == peer) {
                            _messages.update { ms ->
                                ms.map { if (it.out && it.id == ev.id && it.status == "pending") it.copy(status = "delivered") else it }
                            }
                        }
                        is NodeEvent.Updated -> if (ev.peer == peer) reload()
                        is NodeEvent.Typing -> if (ev.peer == peer) setTyping(true)
                        else -> {}
                    }
                }
            }
            runCatching { twocan.call { Json.messages(it.history(peer)) } }
                .onSuccess { history -> _messages.update { (history + it).distinctBy { m -> m.out to m.id } } }
                .onFailure { _error.value = it.userMessage }
            markRead()
        }
    }

    private fun add(m: Message) = _messages.update { ms ->
        if (ms.any { it.out == m.out && it.id == m.id }) ms else ms + m
    }

    /** Loads the conversation again, after messages changed. */
    private suspend fun reload() {
        runCatching { twocan.call { Json.messages(it.history(peer)) } }
            .onSuccess { history ->
                _messages.value = history
                // What is replied to or edited may be gone.
                when (val c = _compose.value) {
                    is Compose.Reply -> if (history.none { it.id == c.to.id }) _compose.value = Compose.Plain
                    is Compose.Edit -> if (history.none { it.id == c.message.id && !it.deleted }) _compose.value = Compose.Plain
                    Compose.Plain -> {}
                }
            }
    }

    private fun setTyping(on: Boolean) {
        typingJob?.cancel()
        _typing.value = on
        if (on) typingJob = viewModelScope.launch {
            delay(TYPING_FOR_MS)
            _typing.value = false
        }
    }

    private suspend fun markRead() {
        runCatching { twocan.call { it.markRead(peer) } }
        twocan.refreshContacts()
    }

    fun startReply(m: Message) { _compose.value = Compose.Reply(m) }
    fun startEdit(m: Message) { _compose.value = Compose.Edit(m) }
    fun endCompose() { _compose.value = Compose.Plain }

    /** Tells the contact we are typing (the Go core throttles it). */
    fun typing() {
        if (_compose.value !is Compose.Edit) viewModelScope.launch { runCatching { twocan.call { it.typing(peer) } } }
    }

    /**
     * Sends text as a plain message, a reply or an edit, as compose says.
     * Returns false (and sets error) if it failed.
     */
    suspend fun send(text: String): Boolean {
        val c = _compose.value
        val result = runCatching {
            when (c) {
                is Compose.Edit -> twocan.call { it.edit(peer, c.message.id, text) }
                is Compose.Reply -> add(twocan.call { Json.message(it.reply(peer, c.to.id, text)) })
                Compose.Plain -> add(twocan.call { Json.message(it.send(peer, text)) })
            }
        }
        result.onSuccess {
            _error.value = null
            _compose.value = Compose.Plain
            if (c is Compose.Edit) reload()
        }.onFailure { _error.value = it.userMessage }
        return result.isSuccess
    }

    fun react(m: Message, emoji: String) = act { it.react(peer, m.id, emoji) }
    fun delete(m: Message, forEveryone: Boolean) = act { it.delete(peer, m.id, forEveryone) }

    /** Runs an action on the node and shows the conversation as it is after it. */
    private fun act(block: (app.twocan.go.mobile.Node) -> Unit) {
        viewModelScope.launch {
            runCatching { twocan.call(block) }
                .onSuccess { _error.value = null; reload() }
                .onFailure { _error.value = it.userMessage }
        }
    }
}

/**
 * The conversation with [peer]. [focus], if set, is a message to scroll to
 * (from search).
 */
@Composable
fun ChatScreen(twocan: Twocan, peer: String, focus: String?, onBack: () -> Unit, onDetails: () -> Unit) {
    val vm: ChatViewModel = viewModel(key = peer) { ChatViewModel(twocan, peer) }
    val messages by vm.messages.collectAsStateWithLifecycle()
    val error by vm.error.collectAsStateWithLifecycle()
    val typing by vm.typing.collectAsStateWithLifecycle()
    val compose by vm.compose.collectAsStateWithLifecycle()
    val contacts by twocan.contacts.collectAsStateWithLifecycle()
    val contact = contacts.firstOrNull { it.id == peer }
    val name = contact?.name ?: ""
    var draft by rememberSaveable(peer) { mutableStateOf("") }
    // The draft from before an edit, restored when the edit ends.
    var savedDraft by rememberSaveable(peer) { mutableStateOf("") }
    val list = rememberLazyListState()
    var focused by rememberSaveable(peer) { mutableStateOf(false) }
    LaunchedEffect(messages.size) {
        if (messages.isEmpty()) return@LaunchedEffect
        val i = if (focus != null && !focused) messages.asReversed().indexOfFirst { it.id == focus } else -1
        if (i >= 0) {
            focused = true
            list.scrollToItem(i)
        } else {
            list.animateScrollToItem(0)
        }
    }
    var sending by remember { mutableStateOf(false) }
    var confirmDelete by remember { mutableStateOf<Message?>(null) }
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val canAct = contact?.accepted == true

    fun endCompose() {
        if (compose is Compose.Edit) {
            draft = savedDraft
            savedDraft = ""
        }
        vm.endCompose()
    }

    Scaffold(
        topBar = {
            BackBar(
                title = {
                    Column {
                        Text(name, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Text(
                            if (typing) "typing…" else contact?.linkText ?: "",
                            style = MaterialTheme.typography.bodySmall,
                            fontStyle = if (typing) FontStyle.Italic else FontStyle.Normal,
                        )
                    }
                },
                onBack = onBack,
                actions = {
                    IconButton(onClick = onDetails) { Icon(Icons.Default.Info, contentDescription = "Connection details") }
                },
            )
        },
    ) { padding ->
        Column(Modifier.fillMaxSize().padding(padding).imePadding()) {
            LazyColumn(
                Modifier.weight(1f).fillMaxWidth(),
                state = list,
                reverseLayout = true,
                verticalArrangement = Arrangement.spacedBy(6.dp),
                contentPadding = PaddingValues(12.dp),
            ) {
                items(messages.asReversed(), key = { (if (it.out) "o" else "i") + it.id }) { m ->
                    val quoted = if (m.replyTo.isEmpty()) null else messages.firstOrNull { it.id == m.replyTo }
                    Bubble(
                        m = m,
                        quote = when {
                            m.replyTo.isEmpty() -> null
                            quoted == null -> "a message no longer here"
                            else -> snippet(quoted, name)
                        },
                        canAct = canAct,
                        onReply = { vm.startReply(m) },
                        onEdit = {
                            if (compose !is Compose.Edit) savedDraft = draft
                            draft = m.text
                            vm.startEdit(m)
                        },
                        onReact = { vm.react(m, it) },
                        onCopy = { context.copyText("Message", m.text) },
                        onDeleteForMe = { vm.delete(m, forEveryone = false) },
                        onDeleteForEveryone = { confirmDelete = m },
                    )
                }
            }
            error?.let {
                Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.padding(horizontal = 16.dp))
            }
            ComposeBar(compose, name, onCancel = ::endCompose)
            ChatInput(
                contact = contact,
                draft = draft,
                onDraft = {
                    draft = it
                    if (it.isNotEmpty()) vm.typing()
                },
                editing = compose is Compose.Edit,
                enabled = !sending,
                onSend = {
                    val text = draft
                    val wasEdit = compose is Compose.Edit
                    sending = true
                    scope.launch {
                        if (vm.send(text) && draft == text) {
                            draft = if (wasEdit) savedDraft else ""
                            savedDraft = ""
                        }
                        sending = false
                    }
                },
            )
        }
    }

    confirmDelete?.let { m ->
        AlertDialog(
            onDismissRequest = { confirmDelete = null },
            title = { Text("Delete for everyone?") },
            text = { Text("The message is replaced by \"This message was deleted\" here and on their device.") },
            confirmButton = {
                TextButton(onClick = { confirmDelete = null; vm.delete(m, forEveryone = true) }) { Text("Delete") }
            },
            dismissButton = { TextButton(onClick = { confirmDelete = null }) { Text("Cancel") } },
        )
    }
}

/** "Name: start of the text" for a quoted message. */
private fun snippet(m: Message, name: String): String {
    val who = if (m.out) "You" else name
    val text = if (m.deleted) "deleted message" else m.text.replace(Regex("\\s+"), " ")
    return "$who: " + if (text.length > 60) text.take(60) + "…" else text
}

/** Says what is being replied to or edited, with a button to stop. */
@Composable
private fun ComposeBar(compose: Compose, name: String, onCancel: () -> Unit) {
    val text = when (compose) {
        Compose.Plain -> return
        is Compose.Reply -> "Replying to " + snippet(compose.to, name)
        is Compose.Edit -> "Editing your message"
    }
    Row(Modifier.fillMaxWidth().padding(start = 16.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(
            text,
            style = MaterialTheme.typography.bodySmall,
            fontStyle = FontStyle.Italic,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f),
        )
        IconButton(onClick = onCancel) { Icon(Icons.Default.Close, contentDescription = "Cancel") }
    }
}

@Composable
private fun ChatInput(
    contact: Contact?,
    draft: String,
    onDraft: (String) -> Unit,
    editing: Boolean,
    enabled: Boolean,
    onSend: () -> Unit,
) {
    val note = when {
        contact == null -> "This contact is gone."
        contact.incoming -> "Accept their request on the contacts screen to chat."
        contact.outgoing -> "You can chat once they accept your request."
        else -> null
    }
    if (note != null) {
        Text(note, Modifier.navigationBarsPadding().padding(16.dp), style = MaterialTheme.typography.bodyMedium)
        return
    }
    Row(
        Modifier.fillMaxWidth().navigationBarsPadding().padding(8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        OutlinedTextField(
            value = draft,
            onValueChange = onDraft,
            placeholder = { Text("Write a message") },
            maxLines = 5,
            modifier = Modifier.weight(1f),
        )
        IconButton(onClick = onSend, enabled = enabled && draft.isNotBlank()) {
            Icon(Icons.AutoMirrored.Filled.Send, contentDescription = if (editing) "Save" else "Send")
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun Bubble(
    m: Message,
    quote: String?,
    canAct: Boolean,
    onReply: () -> Unit,
    onEdit: () -> Unit,
    onReact: (String) -> Unit,
    onCopy: () -> Unit,
    onDeleteForMe: () -> Unit,
    onDeleteForEveryone: () -> Unit,
) {
    var menu by remember { mutableStateOf(false) }
    BoxWithConstraints(Modifier.fillMaxWidth()) {
        Box(
            Modifier
                .widthIn(max = maxWidth * BUBBLE_WIDTH)
                .align(if (m.out) Alignment.CenterEnd else Alignment.CenterStart),
        ) {
            Surface(
                color = if (m.out) MaterialTheme.colorScheme.primaryContainer else MaterialTheme.colorScheme.surfaceVariant,
                shape = MaterialTheme.shapes.medium,
                modifier = Modifier.combinedClickable(
                    onClick = {},
                    onLongClick = { menu = true },
                    onLongClickLabel = "Message options",
                ),
            ) {
                Column(Modifier.padding(horizontal = 12.dp, vertical = 8.dp)) {
                    quote?.let {
                        Text(
                            "» $it",
                            style = MaterialTheme.typography.labelSmall,
                            fontStyle = FontStyle.Italic,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.padding(bottom = 4.dp),
                        )
                    }
                    if (m.deleted) {
                        Text(
                            "This message was deleted",
                            fontStyle = FontStyle.Italic,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    } else {
                        FormattedText(m.text)
                    }
                    val reactions = listOf(m.theirReaction, m.myReaction).filter { it.isNotEmpty() }.joinToString(" ")
                    if (reactions.isNotEmpty()) Text(reactions, modifier = Modifier.padding(top = 4.dp))
                    val mark = when {
                        !m.out || m.deleted -> ""
                        m.read -> " ✓✓"
                        m.delivered -> " ✓"
                        else -> " 🕓"
                    }
                    Text(
                        (if (m.edited && !m.deleted) "edited · " else "") + timeText(m.at) + mark,
                        style = MaterialTheme.typography.labelSmall,
                        modifier = Modifier.align(Alignment.End),
                    )
                }
            }
            DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                val close = { action: () -> Unit -> { menu = false; action() } }
                if (!m.deleted) {
                    if (canAct) {
                        Row(Modifier.padding(horizontal = 8.dp)) {
                            REACTIONS.forEach { e ->
                                TextButton(
                                    onClick = close { onReact(if (m.myReaction == e) "" else e) },
                                    contentPadding = PaddingValues(horizontal = 4.dp),
                                ) { Text(e, style = MaterialTheme.typography.titleLarge) }
                            }
                        }
                        HorizontalDivider()
                    }
                    MenuEntry("Reply", canAct, close(onReply))
                    MenuEntry("Copy text", true, close(onCopy))
                    if (m.out) MenuEntry("Edit", canAct, close(onEdit))
                    HorizontalDivider()
                }
                MenuEntry("Delete for me", true, close(onDeleteForMe))
                if (m.out && !m.deleted) MenuEntry("Delete for everyone", canAct, close(onDeleteForEveryone))
            }
        }
    }
}

@Composable
private fun MenuEntry(label: String, enabled: Boolean, onClick: () -> Unit) {
    DropdownMenuItem(text = { Text(label) }, enabled = enabled, onClick = onClick)
}

private fun timeText(ms: Long): String {
    val day = 24 * 60 * 60 * 1000L
    val fmt = if (System.currentTimeMillis() - ms < day) {
        DateFormat.getTimeInstance(DateFormat.SHORT)
    } else {
        DateFormat.getDateTimeInstance(DateFormat.SHORT, DateFormat.SHORT)
    }
    return fmt.format(Date(ms))
}
