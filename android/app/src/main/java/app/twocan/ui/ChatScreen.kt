package app.twocan.ui

import androidx.compose.foundation.layout.Arrangement
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
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Info
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
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
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/** The share of the conversation's width a bubble may take. */
private const val BUBBLE_WIDTH = 0.8f

/** The conversation with one contact. */
class ChatViewModel(private val twocan: Twocan, val peer: String) : ViewModel() {
    private val _messages = MutableStateFlow<List<Message>>(emptyList())
    /** Oldest first. */
    val messages: StateFlow<List<Message>> = _messages.asStateFlow()
    private val _error = MutableStateFlow<String?>(null)
    /** Why the last send or load failed; null if it did not. */
    val error: StateFlow<String?> = _error.asStateFlow()

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
                            markRead()
                        }
                        is NodeEvent.Delivered -> if (ev.peer == peer) {
                            _messages.update { ms -> ms.map { if (it.out && it.id == ev.id) it.copy(status = "delivered") else it } }
                        }
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

    private suspend fun markRead() {
        runCatching { twocan.call { it.markRead(peer) } }
        twocan.refreshContacts()
    }

    /** Sends text; returns false (and sets error) if it could not be queued. */
    suspend fun send(text: String): Boolean =
        runCatching { twocan.call { Json.message(it.send(peer, text)) } }
            .onSuccess { add(it); _error.value = null }
            .onFailure { _error.value = it.userMessage }
            .isSuccess
}

@Composable
fun ChatScreen(twocan: Twocan, peer: String, onBack: () -> Unit, onDetails: () -> Unit) {
    val vm: ChatViewModel = viewModel(key = peer) { ChatViewModel(twocan, peer) }
    val messages by vm.messages.collectAsStateWithLifecycle()
    val error by vm.error.collectAsStateWithLifecycle()
    val contacts by twocan.contacts.collectAsStateWithLifecycle()
    val contact = contacts.firstOrNull { it.id == peer }
    var draft by rememberSaveable(peer) { mutableStateOf("") }
    val list = rememberLazyListState()
    LaunchedEffect(messages.size) { if (messages.isNotEmpty()) list.animateScrollToItem(0) }
    var sending by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    Scaffold(
        topBar = {
            BackBar(
                title = {
                    Column {
                        Text(contact?.name ?: "", maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Text(contact?.linkText ?: "", style = MaterialTheme.typography.bodySmall)
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
                items(messages.asReversed(), key = { (if (it.out) "o" else "i") + it.id }) { Bubble(it) }
            }
            error?.let {
                Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.padding(horizontal = 16.dp))
            }
            ChatInput(
                contact = contact,
                draft = draft,
                onDraft = { draft = it },
                enabled = !sending,
                onSend = {
                    val text = draft
                    sending = true
                    scope.launch {
                        if (vm.send(text) && draft == text) draft = ""
                        sending = false
                    }
                },
            )
        }
    }
}

@Composable
private fun ChatInput(contact: Contact?, draft: String, onDraft: (String) -> Unit, enabled: Boolean, onSend: () -> Unit) {
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
            Icon(Icons.AutoMirrored.Filled.Send, contentDescription = "Send")
        }
    }
}

@Composable
private fun Bubble(m: Message) {
    BoxWithConstraints(Modifier.fillMaxWidth()) {
        Surface(
            color = if (m.out) MaterialTheme.colorScheme.primaryContainer else MaterialTheme.colorScheme.surfaceVariant,
            shape = MaterialTheme.shapes.medium,
            modifier = Modifier
                .widthIn(max = maxWidth * BUBBLE_WIDTH)
                .align(if (m.out) Alignment.CenterEnd else Alignment.CenterStart),
        ) {
            Column(Modifier.padding(horizontal = 12.dp, vertical = 8.dp)) {
                SelectionContainer { Text(m.text) }
                val mark = when {
                    !m.out -> ""
                    m.delivered -> " ✓"
                    else -> " 🕓"
                }
                Text(
                    timeText(m.at) + mark,
                    style = MaterialTheme.typography.labelSmall,
                    modifier = Modifier.align(Alignment.End),
                )
            }
        }
    }
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
