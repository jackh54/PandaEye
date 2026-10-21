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
        maven("https://repo.papermc.io/repository/maven-public/") {
            content {
                includeGroupAndSubgroups("io.papermc")
                includeGroupAndSubgroups("com.velocitypowered")
                includeGroup("net.md-5")
            }
        }
        maven("https://repo.codemc.io/repository/maven-releases/") {
            content { includeGroup("com.github.retrooper") }
        }
    }
}

rootProject.name = "pandaeye"

include("protocol", "common", "paper", "velocity")
