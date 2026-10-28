package app.twocan

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import app.twocan.ui.AddContactScreen
import app.twocan.ui.CardScreen
import app.twocan.ui.ChatScreen
import app.twocan.ui.ContactsScreen
import app.twocan.ui.DetailsScreen
import app.twocan.ui.SearchScreen
import app.twocan.ui.SettingsScreen
import app.twocan.ui.SetupScreen
import app.twocan.ui.TwocanTheme

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val twocan = (application as TwocanApp).twocan
        setContent {
            TwocanTheme {
                Surface(Modifier.fillMaxSize()) { TwocanUi(twocan) }
            }
        }
    }
}

@Composable
private fun TwocanUi(twocan: Twocan) {
    val state by twocan.state.collectAsStateWithLifecycle()
    val name by twocan.displayName.collectAsStateWithLifecycle()
    // Once the node has run, keep the screens (and where the user was) while
    // it stops in the background and starts again; calls meanwhile fail
    // with "Twocan is not running".
    var ran by rememberSaveable { mutableStateOf(false) }
    if (state == Twocan.State.Running) ran = true
    val s = state
    when {
        s is Twocan.State.Failed -> Centered {
            Text("Twocan could not start", style = MaterialTheme.typography.titleMedium)
            Text(s.error)
            Button(onClick = twocan::start) { Text("Try again") }
            if (twocan.bootstrap.isNotEmpty()) {
                // A bad helper address set in Settings stops the node starting.
                OutlinedButton(onClick = { twocan.bootstrap = ""; twocan.start() }) { Text("Use the built-in helper") }
            }
        }
        s == Twocan.State.Running && name.isEmpty() -> SetupScreen(twocan)
        // One call site, so the navigation state survives a restart.
        ran && name.isNotEmpty() -> Screens(twocan)
        else -> Centered {
            CircularProgressIndicator()
            Text("Starting Twocan…")
        }
    }
}

@Composable
private fun Screens(twocan: Twocan) {
    val nav = rememberNavController()
    NavHost(nav, startDestination = "contacts") {
        composable("contacts") {
            ContactsScreen(
                twocan,
                onOpen = { nav.navigate("chat/$it") },
                onSearch = { nav.navigate("search") },
                onMyCard = { nav.navigate("card") },
                onAdd = { nav.navigate("add") },
                onSettings = { nav.navigate("settings") },
            )
        }
        composable("chat/{id}?msg={msg}") {
            val id = it.arguments?.getString("id").orEmpty()
            val msg = it.arguments?.getString("msg")
            ChatScreen(twocan, id, msg, onBack = nav::popBackStack, onDetails = { nav.navigate("details/$id") })
        }
        composable("search") {
            SearchScreen(
                twocan,
                onOpen = { peer, msg -> nav.navigate("chat/$peer?msg=$msg") },
                onBack = nav::popBackStack,
            )
        }
        composable("details/{id}") {
            DetailsScreen(twocan, it.arguments?.getString("id").orEmpty(), onBack = nav::popBackStack)
        }
        composable("card") { CardScreen(twocan, onBack = nav::popBackStack) }
        composable("add") { AddContactScreen(twocan, onBack = nav::popBackStack) }
        composable("settings") { SettingsScreen(twocan, onBack = nav::popBackStack) }
    }
}

@Composable
private fun Centered(content: @Composable () -> Unit) {
    Column(
        Modifier.fillMaxSize().padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp, Alignment.CenterVertically),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) { content() }
}
