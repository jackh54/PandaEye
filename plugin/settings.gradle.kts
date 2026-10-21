pluginManagement {
    repositories {
        gradlePluginPortal()
        mavenCentral()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        mavenCentral()
        exclusiveContent {
            forRepository { maven("https://repo.papermc.io/repository/maven-public/") }
            filter {
                includeGroupAndSubgroups("io.papermc")
                includeGroupAndSubgroups("com.velocitypowered")
                includeGroup("com.mojang")
                includeGroup("net.md-5")
            }
        }
        exclusiveContent {
            forRepository { maven("https://repo.codemc.io/repository/maven-releases/") }
            filter { includeGroup("com.github.retrooper") }
        }
    }
}

rootProject.name = "pandaeye"

include("protocol", "common", "paper", "velocity")
