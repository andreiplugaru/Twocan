package app.twocan.ui

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Person
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Badge
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import app.twocan.Contact
import app.twocan.Twocan
import app.twocan.userMessage
import kotlinx.coroutines.launch

/** The home screen: contacts and requests, newest first as the core lists them. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ContactsScreen(
    twocan: Twocan,
    onOpen: (String) -> Unit,
    onMyCard: () -> Unit,
    onAdd: () -> Unit,
    onSettings: () -> Unit,
    onSearch: () -> Unit,
) {
    val contacts by twocan.contacts.collectAsStateWithLifecycle()
    val name by twocan.displayName.collectAsStateWithLifecycle()
    val reach by twocan.reachability.collectAsStateWithLifecycle()
    val state by twocan.state.collectAsStateWithLifecycle()
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    var renaming by remember { mutableStateOf<Contact?>(null) }

    fun act(block: suspend () -> Unit) {
        scope.launch {
            runCatching { block() }.onFailure { snackbar.showSnackbar(it.userMessage) }
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Column {
                        Text("Twocan")
                        Text(name, style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    }
                },
                actions = {
                    IconButton(onClick = onSearch) { Icon(Icons.Default.Search, contentDescription = "Search messages") }
                    IconButton(onClick = onMyCard) { Icon(Icons.Default.Person, contentDescription = "My card") }
                    IconButton(onClick = onSettings) { Icon(Icons.Default.Settings, contentDescription = "Settings") }
                },
            )
        },
        floatingActionButton = {
            ExtendedFloatingActionButton(
                onClick = onAdd,
                icon = { Icon(Icons.Default.Add, contentDescription = null) },
                text = { Text("Add contact") },
            )
        },
        bottomBar = {
            Text(
                if (state == Twocan.State.Running) "You are reachable: $reach" else "Connecting…",
                style = MaterialTheme.typography.bodySmall,
                modifier = Modifier.navigationBarsPadding().padding(horizontal = 16.dp, vertical = 8.dp),
            )
        },
        snackbarHost = { SnackbarHost(snackbar) },
    ) { padding ->
        if (contacts.isEmpty()) {
            Box(Modifier.fillMaxSize().padding(padding).padding(32.dp), contentAlignment = Alignment.Center) {
                Text(
                    "No contacts yet.\nShow someone your card (the person icon above), or tap Add contact to scan theirs.",
                    textAlign = TextAlign.Center,
                )
            }
        } else {
            LazyColumn(Modifier.fillMaxSize().padding(padding)) {
                items(contacts, key = { it.id }) { c ->
                    ContactRow(
                        c,
                        onOpen = { onOpen(c.id) },
                        onRename = { renaming = c },
                        onAccept = { act { twocan.call { it.accept(c.id) } } },
                        onReject = { act { twocan.call { it.reject(c.id) } } },
                    )
                    HorizontalDivider()
                }
            }
        }
    }

    renaming?.let { c ->
        RenameDialog(
            c,
            onDismiss = { renaming = null },
            onRename = { nick ->
                renaming = null
                act { twocan.call { it.rename(c.id, nick) } }
            },
        )
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun ContactRow(c: Contact, onOpen: () -> Unit, onRename: () -> Unit, onAccept: () -> Unit, onReject: () -> Unit) {
    ListItem(
        modifier = Modifier.combinedClickable(
            onClick = onOpen,
            onLongClick = if (c.accepted) onRename else null,
            onLongClickLabel = "Rename",
        ),
        leadingContent = { Dot(c.connected) },
        headlineContent = { Text(c.name, maxLines = 1, overflow = TextOverflow.Ellipsis) },
        supportingContent = { Text(c.linkText) },
        trailingContent = {
            when {
                c.incoming -> Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedButton(onClick = onReject) { Text("Reject") }
                    Button(onClick = onAccept) { Text("Accept") }
                }
                c.unread > 0 -> Badge { Text("${c.unread}") }
            }
        },
    )
}

@Composable
private fun Dot(connected: Boolean) {
    val color = if (connected) Color(0xFF2E7D32) else MaterialTheme.colorScheme.outlineVariant
    Box(Modifier.size(12.dp).background(color, CircleShape))
}

@Composable
private fun RenameDialog(c: Contact, onDismiss: () -> Unit, onRename: (String) -> Unit) {
    var nick by rememberSaveable { mutableStateOf(c.name) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Rename") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("Only you see this name. Leave it empty to use the name they chose.")
                OutlinedTextField(nick, { nick = it }, singleLine = true, modifier = Modifier.fillMaxWidth())
            }
        },
        confirmButton = { TextButton(onClick = { onRename(nick) }) { Text("Save") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}
